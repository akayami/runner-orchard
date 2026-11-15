#!/usr/bin/env python3
"""Wait for lid closure, build both Apple platforms, and record continuity."""
import argparse
import datetime
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

from lid_probe import capture, evaluate, sample


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seconds", type=int, default=600)
    parser.add_argument("--minimum-closed", type=int, default=120)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.seconds <= args.minimum_closed or args.minimum_closed < 1:
        parser.error("seconds must exceed positive minimum-closed")
    args.output.mkdir(parents=True, exist_ok=False)
    started = datetime.datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    deadline = time.time() + args.seconds
    samples, build_samples = [], []
    process = None
    build_result = None
    interrupted = False
    print("Ready. On AC, close the lid for about three minutes. Builds start when closure is detected.", flush=True)
    try:
        with (args.output / "build.log").open("w") as log, (args.output / "samples.jsonl").open("w") as stream:
            while time.time() < deadline:
                item = sample()
                samples.append(item)
                stream.write(json.dumps(item) + "\n")
                stream.flush()
                if process is None and item["lid_closed"] is True and item["ac_power"]:
                    command = [sys.executable, str(Path(__file__).with_name("validate_builds.py"))]
                    process = subprocess.Popen(command, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
                    print("Lid closed: native builds started.", flush=True)
                if process is not None and build_result is None:
                    build_samples.append(item)
                    build_result = process.poll()
                if build_result is not None and evaluate(samples, args.minimum_closed)["passed"]:
                    break
                time.sleep(5)
    except KeyboardInterrupt:
        interrupted = True
    finally:
        if process is not None and process.poll() is None:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
        result = evaluate(samples, args.minimum_closed)
        result.update(build_exit_code=build_result, interrupted=interrupted,
                      build_samples=len(build_samples),
                      build_entirely_closed=bool(build_samples) and all(s["lid_closed"] is True for s in build_samples))
        try:
            events = [line for line in capture("/usr/bin/pmset", "-g", "log").splitlines()
                      if line[:19] >= started and "Entering Sleep state" in line]
            result["sleep_events"] = events
        except (RuntimeError, subprocess.SubprocessError):
            events = ["Unable to verify sleep logs"]
            result["sleep_events"] = events
        result["passed"] = (result["passed"] and build_result == 0 and result["build_entirely_closed"]
                            and not events and not interrupted)
        (args.output / "result.json").write_text(json.dumps(result, indent=2) + "\n")
        print(json.dumps(result, indent=2), flush=True)
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
