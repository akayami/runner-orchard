#!/usr/bin/env python3
"""Bounded, experimental no-monitor lid trial. Run separately from lid_probe.py."""

import argparse
import os
import platform
import plistlib
import signal
import subprocess
import time


def on_ac():
    result = subprocess.run(["/usr/bin/pmset", "-g", "batt"],
                            capture_output=True, text=True, timeout=10, check=True)
    return "Now drawing from 'AC Power'" in result.stdout


def restore():
    subprocess.run(["/usr/bin/pmset", "-a", "disablesleep", "0"], check=True, timeout=10)


def interrupted(signum, frame):
    raise KeyboardInterrupt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seconds", type=int, default=300)
    args = parser.parse_args()
    if platform.system() != "Darwin" or os.getuid() != 0:
        parser.error("requires macOS and sudo; do not install as a permanent service")
    if not 30 <= args.seconds <= 900:
        parser.error("trial duration must be between 30 and 900 seconds")
    root = plistlib.loads(subprocess.check_output(
        ["/usr/sbin/ioreg", "-a", "-r", "-c", "IOPMrootDomain", "-d", "1"], timeout=10))[0]
    if root.get("SleepDisabled") is not False:
        parser.error("SleepDisabled must be known false before starting; refusing to overwrite existing policy")
    if not on_ac():
        parser.error("connect AC power first")
    signal.signal(signal.SIGTERM, interrupted)
    # Independent root-owned watchdog survives parent crash/SIGKILL. It restores
    # normal sleep on timeout, loss of AC, or inability to read power state.
    watchdog = subprocess.Popen([
        "/bin/sh", "-c", '''
remaining="$1"
while [ "$remaining" -gt 0 ]; do
  /bin/sleep 2
  power=$(/usr/bin/pmset -g batt) || break
  case "$power" in *"Now drawing from 'AC Power'"*) ;; *) break ;; esac
  remaining=$((remaining - 2))
done
/usr/bin/pmset -a disablesleep 0
''', "power-trial-watchdog", str(args.seconds)], start_new_session=True,
        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        subprocess.run(["/usr/bin/pmset", "-a", "disablesleep", "1"], check=True, timeout=10)
        print(f"Experimental sleep override active for at most {args.seconds}s. Independent watchdog armed.", flush=True)
        print("Keep on a ventilated surface. Removing AC ends the trial. Reopen lid before timeout.", flush=True)
        deadline = time.monotonic() + args.seconds
        while time.monotonic() < deadline and watchdog.poll() is None:
            time.sleep(1)
    except KeyboardInterrupt:
        pass
    finally:
        # Leave the watchdog running if restoration fails.
        restore()
        if watchdog.poll() is None:
            os.killpg(watchdog.pid, signal.SIGTERM)
        watchdog.wait()
        print("Normal system sleep restored (disablesleep=0).", flush=True)


if __name__ == "__main__":
    main()
