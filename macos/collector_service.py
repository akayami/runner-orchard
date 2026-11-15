#!/usr/bin/env python3
"""Install the unprivileged telemetry collector independently of the build runner."""
import argparse
import json
import os
from pathlib import Path
import platform
import plistlib
import shutil
import subprocess

LABEL = "io.runner-orchard.collector"
BASE = Path.home() / ".local/share/runner-orchard/collector"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["install", "status", "uninstall"])
    parser.add_argument("--binary", type=Path)
    parser.add_argument("--config", type=Path)
    args = parser.parse_args()
    if platform.system() != "Darwin" or os.getuid() == 0:
        parser.error("run as the regular macOS login user, never root")
    plist = Path.home() / "Library/LaunchAgents" / f"{LABEL}.plist"
    target = f"gui/{os.getuid()}/{LABEL}"
    if args.command == "status":
        return subprocess.run(["launchctl", "print", target]).returncode
    loaded = subprocess.run(["launchctl", "print", target], capture_output=True).returncode == 0
    if args.command == "uninstall":
        if loaded:
            subprocess.run(["launchctl", "bootout", target], check=True)
        plist.unlink(missing_ok=True)
        print(f"Collector unloaded. Private config/binary retained at {BASE} for review.")
        return 0
    if not args.binary or not args.config:
        parser.error("install requires --binary and --config")
    config = json.loads(args.config.read_text())
    if not isinstance(config.get("endpoint"), str):
        parser.error("config needs endpoint")
    if loaded:
        subprocess.run(["launchctl", "bootout", target], check=True)
    BASE.mkdir(parents=True, exist_ok=True, mode=0o700)
    BASE.chmod(0o700)
    binary = BASE / "orchard"
    if args.binary.resolve() != binary.resolve():
        shutil.copyfile(args.binary, binary)
    binary.chmod(0o700)
    saved = BASE / "config.json"
    saved.write_text(json.dumps(config, indent=2) + "\n")
    saved.chmod(0o600)
    plist.parent.mkdir(parents=True, exist_ok=True)
    plist.write_bytes(plistlib.dumps({
        "Label": LABEL, "ProgramArguments": [str(binary), "collect", "-config", str(saved)],
        "RunAtLoad": True, "KeepAlive": True, "ThrottleInterval": 30, "Nice": 10,
        "ProcessType": "Background",
        "StandardOutPath": str(BASE / "collector.log"),
        "StandardErrorPath": str(BASE / "collector.log"),
    }))
    subprocess.run(["launchctl", "bootstrap", f"gui/{os.getuid()}", str(plist)], check=True)
    print("Collector installed. Existing runners and power services were not changed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
