// Native root service: keep a logged-in CI host awake only on external power.
// No Homebrew/Python interpreter runs as root. Never execute user-controlled code.
import Foundation
import IOKit
import IOKit.ps
import Darwin

let stateDirectory = "/Library/Application Support/RunnerOrchard"
let enabledPath = stateDirectory + "/enabled"
let uidPath = stateDirectory + "/uid"
let statusPath = stateDirectory + "/status.json"

func command(_ path: String, _ arguments: [String]) -> Bool {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: path)
    process.arguments = arguments
    process.standardOutput = FileHandle.nullDevice
    process.standardError = FileHandle.standardError
    do {
        try process.run()
        // No pipe to block; pmset/launchctl are local, fixed commands.
        let deadline = Date().addingTimeInterval(5)
        while process.isRunning && Date() < deadline { Thread.sleep(forTimeInterval: 0.05) }
        if process.isRunning { process.terminate(); return false }
        return process.terminationStatus == 0
    } catch { return false }
}

func rootProperty(_ key: String) -> Bool? {
    let service = IOServiceGetMatchingService(kIOMainPortDefault, IOServiceMatching("IOPMrootDomain"))
    guard service != 0 else { return nil }
    defer { IOObjectRelease(service) }
    return IORegistryEntryCreateCFProperty(service, key as CFString, kCFAllocatorDefault, 0)?
        .takeRetainedValue() as? Bool
}

func acPower() -> Bool {
    guard let info = IOPSCopyPowerSourcesInfo()?.takeRetainedValue(),
          let source = IOPSGetProvidingPowerSourceType(info)?.takeUnretainedValue() else { return false }
    return source as String == kIOPSACPowerValue
}

func trustedFile(_ path: String) -> Bool {
    guard let attributes = try? FileManager.default.attributesOfItem(atPath: path),
          let owner = attributes[.ownerAccountID] as? NSNumber,
          let mode = attributes[.posixPermissions] as? NSNumber,
          attributes[.type] as? FileAttributeType == .typeRegular else { return false }
    return owner.intValue == 0 && mode.intValue & 0o022 == 0
}

func configuredUID() -> UInt32? {
    guard trustedFile(uidPath), let text = try? String(contentsOfFile: uidPath, encoding: .utf8),
          let uid = UInt32(text.trimmingCharacters(in: .whitespacesAndNewlines)), uid >= 501 else { return nil }
    return uid
}

func writeStatus(_ object: [String: Any]) {
    if let data = try? JSONSerialization.data(withJSONObject: object, options: [.prettyPrinted, .sortedKeys]) {
        try? data.write(to: URL(fileURLWithPath: statusPath), options: .atomic)
    }
}

guard getuid() == 0 else { fputs("ApplePower requires root\n", stderr); exit(1) }
if CommandLine.arguments.contains("--restore") {
    exit(command("/usr/bin/pmset", ["-a", "disablesleep", "0"]) ? 0 : 1)
}
guard CommandLine.arguments == [CommandLine.arguments[0], "--run"] else {
    fputs("Usage: apple-runner-power --run | --restore\n", stderr); exit(2)
}

// Reconcile actual OS state each tick, including changes made by other utilities.
// Launchd restarts us after a crash; SIGTERM restores before a clean shutdown.
signal(SIGTERM, SIG_IGN)
signal(SIGINT, SIG_IGN)
let termination = DispatchSource.makeSignalSource(signal: SIGTERM, queue: .main)
let interrupt = DispatchSource.makeSignalSource(signal: SIGINT, queue: .main)
func stop() {
    let restored = command("/usr/bin/pmset", ["-a", "disablesleep", "0"])
    writeStatus(["updated": Date().timeIntervalSince1970, "active": false,
                 "reason": "service stopped", "restored": restored])
    exit(restored ? 0 : 1)
}
termination.setEventHandler { stop() }
interrupt.setEventHandler { stop() }
termination.resume()
interrupt.resume()
let timer = DispatchSource.makeTimerSource(queue: .main)
timer.schedule(deadline: .now(), repeating: .seconds(2))
var lastReason = ""
timer.setEventHandler {
    let enabled = trustedFile(enabledPath)
    let ac = acPower()
    let uid = configuredUID()
    let session = uid.map { command("/bin/launchctl", ["print", "gui/\($0)"]) } ?? false
    let desired = enabled && ac && session
    let reason = !enabled ? "paused" : !ac ? "battery or unknown power" : !session ? "CI user logged out" : "AC and CI session"
    let current = rootProperty("SleepDisabled")
    var success = true
    if current != desired {
        success = command("/usr/bin/pmset", ["-a", "disablesleep", desired ? "1" : "0"])
    }
    let actual = rootProperty("SleepDisabled")
    writeStatus(["updated": Date().timeIntervalSince1970, "active": actual ?? false,
                 "desired": desired, "ac": ac, "session": session, "reason": reason,
                 "healthy": success && actual == desired, "experimental_no_monitor": true])
    if reason != lastReason || !success {
        print("Apple runner power: \(reason), desired=\(desired), healthy=\(success && actual == desired)")
        fflush(stdout)
        lastReason = reason
    }
}
timer.resume()
dispatchMain()
