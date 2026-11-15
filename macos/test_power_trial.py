import plistlib
import subprocess
import sys
import unittest
from unittest.mock import MagicMock, patch

import power_trial


class PowerTrialTests(unittest.TestCase):
    def invoke(self, *, disabled=False, ac=True, enable_error=False, restore_error=False):
        watchdog = MagicMock()
        watchdog.pid = 12345
        watchdog.poll.return_value = None
        calls = []

        def run(args, **kwargs):
            calls.append(args)
            if args[-1] == "1" and enable_error:
                raise subprocess.TimeoutExpired(args, 10)
            if args[-1] == "0" and restore_error:
                raise subprocess.TimeoutExpired(args, 10)
            return subprocess.CompletedProcess(args, 0)

        with (patch.object(sys, "argv", ["power_trial.py", "--seconds", "30"]),
              patch.object(power_trial.platform, "system", return_value="Darwin"),
              patch.object(power_trial.os, "getuid", return_value=0),
              patch.object(power_trial.subprocess, "check_output",
                           return_value=plistlib.dumps([{"SleepDisabled": disabled}])),
              patch.object(power_trial, "on_ac", return_value=ac),
              patch.object(power_trial.signal, "signal"),
              patch.object(power_trial.subprocess, "Popen", return_value=watchdog) as spawn,
              patch.object(power_trial.subprocess, "run", side_effect=run),
              patch.object(power_trial.time, "monotonic", side_effect=[0, 31]),
              patch.object(power_trial.os, "killpg") as kill):
            try:
                power_trial.main()
            except (SystemExit, subprocess.TimeoutExpired) as error:
                return calls, spawn.called, kill.called, error
        return calls, spawn.called, kill.called, None

    def test_refuses_existing_override_without_mutation(self):
        calls, spawned, killed, error = self.invoke(disabled=True)
        self.assertIsInstance(error, SystemExit)
        self.assertEqual(calls, [])
        self.assertFalse(spawned)

    def test_refuses_battery_without_mutation(self):
        calls, spawned, killed, error = self.invoke(ac=False)
        self.assertIsInstance(error, SystemExit)
        self.assertEqual(calls, [])
        self.assertFalse(spawned)

    def test_normal_completion_restores_and_retires_watchdog(self):
        calls, spawned, killed, error = self.invoke()
        self.assertIsNone(error)
        self.assertEqual([c[-1] for c in calls], ["1", "0"])
        self.assertTrue(spawned and killed)

    def test_failed_enable_still_restores(self):
        calls, spawned, killed, error = self.invoke(enable_error=True)
        self.assertEqual([c[-1] for c in calls], ["1", "0"])
        self.assertIsInstance(error, subprocess.TimeoutExpired)
        self.assertTrue(killed)

    def test_failed_restore_keeps_independent_watchdog(self):
        calls, spawned, killed, error = self.invoke(restore_error=True)
        self.assertIsInstance(error, subprocess.TimeoutExpired)
        self.assertTrue(spawned)
        self.assertFalse(killed)


if __name__ == "__main__":
    unittest.main()
