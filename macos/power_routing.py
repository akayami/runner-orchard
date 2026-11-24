#!/usr/bin/env python3
"""AC-only admission for one enrolled macOS runner. Runs independently of its listener."""
import argparse
import json
import os
from pathlib import Path
import plistlib
import re
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.request

BASE = Path.home() / ".local/share/runner-orchard/power-routing"
AGENT = "io.runner-orchard.power-routing"


def on_ac():
    result = subprocess.run(["/usr/bin/pmset", "-g", "batt"], capture_output=True, text=True)
    return result.returncode == 0 and "drawing from 'AC Power'" in result.stdout


def local_busy():
    result = subprocess.run(["/bin/ps", "-axo", "comm="], capture_output=True, text=True)
    return result.returncode != 0 or any(Path(line.strip()).name == "Runner.Worker" for line in result.stdout.splitlines())


def write(path, data):
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as tmp:
        tmp.write(data)
    os.replace(tmp.name, path)


def launch(*args, check=True):
    return subprocess.run(["/bin/launchctl", *map(str, args)], capture_output=True, check=check)


class Routing:
    def __init__(self, config):
        self.root = Path(config["root"])
        self.plist = Path(config["plist"])
        self.domain = f"gui/{os.getuid()}"
        self.service = self.domain + "/" + self.plist.stem
        self.url = config["endpoint"]
        self.runner_id = config["runner_id"]
        self.token = (BASE / "token").read_text().strip()
        self.tls = ssl.create_default_context(cafile=str(BASE / "ca.pem"))
        self.stopped = BASE / "power-stopped"
        self.idle_since = None
        self.last_state = None
        self.next_check = 0

    def remote(self, desired):
        req = urllib.request.Request(self.url, data=json.dumps({"eligible": desired}).encode(),
                                     headers={"Content-Type": "application/json", "Authorization": "Bearer " + self.token})
        with urllib.request.urlopen(req, context=self.tls, timeout=8) as resp:
            status = json.load(resp)
        if status.get("runner_id") != self.runner_id or status.get("eligible") is not desired:
            raise ValueError("unexpected runner identity or eligibility")
        return status

    def step(self, now):
        ac = on_ac()
        manual = (self.root / ".drained").exists()
        loaded = launch("print", self.service, check=False).returncode == 0
        desired = ac and not manual and loaded
        state = (ac, manual, loaded)
        if state == self.last_state and now < self.next_check:
            return
        self.last_state = None  # retry failures; direction can change between polls
        try:
            response = self.remote(desired)
        except (OSError, ValueError):
            # The console may be unreachable while GitHub is reachable. Close an
            # idle battery listener locally even if label withdrawal must retry.
            if not ac and loaded and not manual:
                self.stop_when_idle(now, False, grace=60)
            else:
                self.idle_since = None
            raise
        self.last_state = state
        self.next_check = now + 45
        if manual:
            self.idle_since = None
            return  # explicit drain/resume owns the listener
        if ac:
            self.idle_since = None
            if self.stopped.exists() and on_ac() and not (self.root / ".drained").exists():
                if not loaded:
                    launch("bootstrap", self.domain, self.plist)
                    self.next_check = 0
                self.stopped.unlink(missing_ok=True)
            return
        if not loaded:
            self.idle_since = None
            return
        # Withdraw the routing label even when busy. Confirm both GitHub and
        # the native worker are idle throughout a grace interval before stopping.
        self.next_check = now + 4
        self.stop_when_idle(now, response["busy"], grace=20)

    def stop_when_idle(self, now, github_busy, grace):
        if github_busy or local_busy():
            self.idle_since = None
            return
        if self.idle_since is None:
            self.idle_since = now
        elif now - self.idle_since >= grace and not on_ac() and not local_busy() and not (self.root / ".drained").exists():
            self.stopped.touch(mode=0o600)
            launch("bootout", self.service)
            self.idle_since = None


def install(args):
    root = args.runner_dir.resolve()
    runner = json.loads((root / ".runner").read_text(encoding="utf-8-sig"))
    config = json.loads((root / "machine-config.json").read_text())
    name = runner["agentName"]
    if config.get("name") != name or "apple-build" not in config.get("labels", []):
        raise SystemExit("Expected a registered Apple build runner")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+", name) or not re.fullmatch(r"https://[A-Za-z0-9.:-]+", args.console):
        raise SystemExit("Invalid runner or HTTPS console name")
    label = "actions.runner." + config["organization"] + "." + name
    agents = Path.home() / "Library/LaunchAgents"
    plist = agents / (label + ".plist")
    original = plistlib.loads(plist.read_bytes())
    if original["Label"] != label or original["ProgramArguments"][-1] != str(root / "runsvc.sh"):
        raise SystemExit("Runner LaunchAgent does not match registration")
    token = sys.stdin.readline().strip()
    if not re.fullmatch(r"[0-9a-fA-F]{64}", token):
        raise SystemExit("Expected host-specific lifecycle token on stdin")
    settings = {"root": str(root), "plist": str(plist), "runner_id": runner["agentId"],
                "endpoint": args.console + "/api/mac-workers/" + name}
    BASE.mkdir(mode=0o700, parents=True, exist_ok=True)
    # Validate the CA before replacing a working installation.
    ssl.create_default_context(cafile=str(args.ca))
    write(BASE / "ca.pem", args.ca.read_bytes())
    write(BASE / "token", token.encode())
    write(BASE / "config.json", json.dumps(settings).encode())
    script = BASE / "power_routing.py"
    write(script, Path(__file__).read_bytes())
    # The launch gate rechecks AC before every listener startup, including login
    # after reboot. Updating the plist on disk leaves an active job untouched.
    original["ProgramArguments"] = ["/usr/bin/python3", str(script), "gate", str(root / "runsvc.sh")]
    write(plist, plistlib.dumps(original))
    service = {"Label": AGENT, "ProgramArguments": ["/usr/bin/python3", str(script), "watch"],
               "RunAtLoad": True, "KeepAlive": True, "ThrottleInterval": 10,
               "StandardErrorPath": str(BASE / "watch.log")}
    agent = agents / (AGENT + ".plist")
    write(agent, plistlib.dumps(service))
    domain = f"gui/{os.getuid()}"
    if launch("print", domain+"/"+AGENT, check=False).returncode == 0:
        launch("bootout", domain+"/"+AGENT)
    target = domain + "/" + AGENT
    launch("enable", target)
    launch("bootstrap", domain, agent)
    launch("kickstart", target)
    for _ in range(20):
        state = launch("print", target).stdout.splitlines()
        if any(line == b"\tstate = running" for line in state):
            return
        time.sleep(1)
    raise RuntimeError("Power agent did not start; inspect launchctl and watch.log")


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    setup = commands.add_parser("install")
    setup.add_argument("--runner-dir", type=Path, required=True)
    setup.add_argument("--console", required=True)
    setup.add_argument("--ca", type=Path, required=True)
    gate = commands.add_parser("gate")
    gate.add_argument("listener", type=Path)
    commands.add_parser("watch")
    args = parser.parse_args()
    if sys.platform != "darwin" or not os.getuid():
        parser.error("Run in the macOS runner user's GUI session")
    if args.command == "install":
        install(args)
    elif args.command == "gate":
        while not on_ac() or (args.listener.parent / ".drained").exists():
            time.sleep(3)
        os.execv(args.listener, [str(args.listener)])
    else:
        routing = Routing(json.loads((BASE / "config.json").read_text()))
        last_error = None
        while True:
            try:
                routing.step(time.monotonic())
                last_error = None
            except (OSError, ValueError, subprocess.SubprocessError) as error:
                routing.last_state = None
                if type(error) != last_error:
                    print("Power routing retry:", type(error).__name__, file=sys.stderr, flush=True)
                last_error = type(error)
            time.sleep(3)


if __name__ == "__main__":
    main()
