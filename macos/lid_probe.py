#!/usr/bin/env python3
"""Measure continuous closed-lid operation; never change power settings."""

import argparse
import datetime as dt
import json
import platform
import plistlib
from pathlib import Path
import subprocess
import time


def capture(*args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=15)
    if result.returncode:
        raise RuntimeError(f"{args[0]} failed: {result.stderr.strip()}")
    return result.stdout


def sample():
    roots = plistlib.loads(subprocess.check_output(
        ["/usr/sbin/ioreg", "-a", "-r", "-c", "IOPMrootDomain", "-d", "1"], timeout=15))
    root = roots[0]
    battery = capture("/usr/bin/pmset", "-g", "batt")
    return {"time": time.time(), "lid_closed": root.get("AppleClamshellState"),
            "lid_causes_sleep": root.get("AppleClamshellCausesSleep"),
            "sleep_disabled": root.get("SleepDisabled"),
            "ac_power": "Now drawing from 'AC Power'" in battery}


def evaluate(samples, minimum_closed=120, max_gap=15):
    """Only adjacent, known-closed AC samples count toward a continuous interval."""
    longest = current = 0
    gaps = []
    for previous, item in zip(samples, samples[1:]):
        gap = item["time"] - previous["time"]
        if gap <= 0 or gap > max_gap:
            gaps.append(gap)
        if (0 < gap <= max_gap and previous["lid_closed"] is True
                and item["lid_closed"] is True and previous["ac_power"] is True
                and item["ac_power"] is True):
            current += gap
            longest = max(longest, current)
        else:
            current = 0
    return {"passed": longest >= minimum_closed and not gaps,
            "longest_continuous_closed_ac_seconds": round(longest, 2),
            "heartbeat_gaps_seconds": gaps, "required_closed_seconds": minimum_closed,
            "meaning": "Local continuity only; verify remote reachability and a real build separately."}


def positive(value):
    number = int(value)
    if number <= 0:
        raise argparse.ArgumentTypeError("must be positive")
    return number


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seconds", type=positive, default=300)
    parser.add_argument("--minimum-closed", type=positive, default=120)
    parser.add_argument("--output", type=Path, required=True,
                        help="New directory for evidence; existing directories are refused")
    args = parser.parse_args()
    if platform.system() != "Darwin":
        parser.error("requires macOS")
    if args.seconds < args.minimum_closed + 20:
        parser.error("allow at least 20 extra seconds to close and reopen the lid")
    args.output.mkdir(parents=True, exist_ok=False)
    start = time.time()
    # UTC for metadata; local time for matching pmset's local timestamps.
    metadata = {"started_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
                "started_local": dt.datetime.now().strftime("%Y-%m-%d %H:%M:%S"),
                "macos": platform.mac_ver()[0], "architecture": platform.machine(),
                "settings": capture("/usr/bin/pmset", "-g", "custom"),
                "displays": json.loads(capture("/usr/sbin/system_profiler", "SPDisplaysDataType", "-json"))}
    (args.output / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    print(f"Recording for {args.seconds}s. Close the lid for at least {args.minimum_closed}s, then reopen.", flush=True)
    print("If the Mac sleeps, reopen the lid. That is a failed test, not a lost report.", flush=True)
    samples = []
    interrupted = False
    try:
        with (args.output / "samples.jsonl").open("w") as stream:
            while True:
                item = sample()
                samples.append(item)
                stream.write(json.dumps(item) + "\n")
                stream.flush()
                if time.time() - start >= args.seconds:
                    break
                time.sleep(5)
    except KeyboardInterrupt:
        interrupted = True
    finally:
        result = evaluate(samples, args.minimum_closed)
        result["interrupted"] = interrupted
        if interrupted:
            result["passed"] = False
        # Preserve only sleep transitions from this test, rather than months of logs.
        try:
            lines = capture("/usr/bin/pmset", "-g", "log").splitlines()
            events = [line for line in lines if line[:19] >= metadata["started_local"]
                      and "Entering Sleep state" in line]
            (args.output / "sleep-events.txt").write_text("\n".join(events) + "\n")
            result["sleep_events"] = events
            if events:
                result["passed"] = False
        except (RuntimeError, subprocess.TimeoutExpired) as error:
            result["passed"] = False
            result["sleep_log_error"] = str(error)
        (args.output / "result.json").write_text(json.dumps(result, indent=2) + "\n")
        print(json.dumps(result, indent=2), flush=True)
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
