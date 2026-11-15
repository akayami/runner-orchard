#!/usr/bin/env python3
"""Local health check. No GitHub admin credentials are needed."""
import argparse
import json
import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import sys
import time

BASE = Path.home() / ".local/share/runner-orchard/runner"
LABEL = "io.runner-orchard-health"


def snapshot():
    problems = []
    free = shutil.disk_usage(Path.home()).free // 1024 ** 3
    if free < 30:
        problems.append("Less than 30 GiB free")
    try:
        registration = json.loads((BASE / ".runner").read_text(encoding="utf-8-sig"))
        value = (BASE / ".service").read_text().strip()
        label = Path(value).stem if value.startswith("/") else value
        state = subprocess.run(["launchctl", "print", f"gui/{os.getuid()}/{label}"],
                               capture_output=True, text=True, timeout=10)
        drained = (BASE / ".drained").exists()
        if not drained and (state.returncode or "state = running" not in state.stdout):
            problems.append("Runner service is not running")
    except (OSError, ValueError, subprocess.SubprocessError):
        registration, drained = {}, False
        problems.append("Runner registration/service unavailable")
    power = {}
    options = BASE / ".health-options.json"
    require_power = json.loads(options.read_text()).get("require_power", True) if options.exists() else True
    try:
        power = json.loads(Path("/Library/Application Support/RunnerOrchard/status.json").read_text())
        if require_power and (time.time() - power["updated"] > 15 or not power["healthy"]):
            problems.append("Power supervisor is stale or unhealthy")
    except (OSError, ValueError, KeyError):
        if require_power:
            problems.append("Power supervisor is not reporting")
    return {"updated": time.time(), "healthy": not problems, "problems": problems,
            "runner": registration.get("agentName"), "drained": drained,
            "free_gib": free, "power": power}


def install(require_power=True):
    if os.getuid() == 0:
        raise RuntimeError("Install health checks as the CI account")
    BASE.mkdir(parents=True, exist_ok=True)
    (BASE / ".health-options.json").write_text(json.dumps({"require_power": require_power}))
    target = BASE / "health.py"
    if Path(__file__).resolve() != target.resolve():
        shutil.copyfile(__file__, target)
    path = Path.home() / "Library/LaunchAgents" / f"{LABEL}.plist"
    if path.exists():
        subprocess.run(["launchctl", "bootout", f"gui/{os.getuid()}/{LABEL}"],
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(plistlib.dumps({"Label": LABEL,
        "ProgramArguments": [shutil.which("python3") or sys.executable, str(target), "--record"],
        "RunAtLoad": True, "StartInterval": 60,
        "StandardOutPath": str(BASE / "health.log"), "StandardErrorPath": str(BASE / "health.log")}))
    subprocess.run(["launchctl", "bootstrap", f"gui/{os.getuid()}", str(path)], check=True)


def uninstall():
    subprocess.run(["launchctl", "bootout", f"gui/{os.getuid()}/{LABEL}"],
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    (Path.home() / "Library/LaunchAgents" / f"{LABEL}.plist").unlink(missing_ok=True)
    for name in ("health.py", "health.json", ".health-options.json"):
        (BASE / name).unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--install", action="store_true")
    parser.add_argument("--uninstall", action="store_true")
    parser.add_argument("--record", action="store_true")
    args = parser.parse_args()
    if args.install:
        install()
        return 0
    if args.uninstall:
        uninstall()
        return 0
    result = snapshot()
    if args.record:
        path = BASE / "health.json"
        previous = json.loads(path.read_text()) if path.exists() else {}
        temporary = BASE / "health.json.tmp"
        temporary.write_text(json.dumps(result, indent=2) + "\n")
        temporary.replace(path)
        if previous.get("problems") != result["problems"]:
            print(json.dumps({"updated": result["updated"], "problems": result["problems"]}), flush=True)
    else:
        print(json.dumps(result, indent=2))
    return 0 if result["healthy"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
