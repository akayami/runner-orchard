#!/usr/bin/env python3
"""Verify AC/battery transitions restore the real OS sleep flag; no mutations."""
import argparse
import json
from pathlib import Path
import time

from lid_probe import sample


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seconds", type=int, default=120)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    readings = []
    battery_seen = False
    reconnected_at = None
    deadline = time.time() + args.seconds
    print("Recording. With lid OPEN: unplug AC for 15 seconds, then reconnect for 15 seconds.", flush=True)
    while time.time() < deadline:
        item = sample()
        readings.append(item)
        if not item["ac_power"]:
            battery_seen = True
            reconnected_at = None
        elif battery_seen:
            if reconnected_at is None:
                reconnected_at = time.time()
            elif time.time() - reconnected_at >= 12:
                break
        (args.output / "samples.json").write_text(json.dumps(readings, indent=2) + "\n")
        time.sleep(1)
    groups = []
    for item in readings:
        if not groups or groups[-1][-1]["ac_power"] != item["ac_power"]:
            groups.append([])
        groups[-1].append(item)
    results = []
    for group in groups:
        # Give the two-second supervisor loop up to six seconds to reconcile.
        stable = [s for s in group if s["time"] - group[0]["time"] >= 6]
        results.append({"ac": group[0]["ac_power"], "duration": group[-1]["time"] - group[0]["time"],
                        "healthy": bool(stable) and all(s["sleep_disabled"] is s["ac_power"] for s in stable)})
    passed = (any(not r["ac"] and r["healthy"] for r in results)
              and len(results) >= 3 and results[-1]["ac"] and results[-1]["healthy"]
              and all(r["healthy"] for r in results if r["duration"] >= 6))
    report = {"passed": passed, "intervals": results}
    (args.output / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2), flush=True)
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
