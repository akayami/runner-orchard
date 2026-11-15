import unittest

from lid_probe import evaluate


def readings(times, closed=True, ac=True):
    return [{"time": t, "lid_closed": closed, "ac_power": ac} for t in times]


class LidEvidenceTests(unittest.TestCase):
    def test_continuous_closed_lid(self):
        self.assertTrue(evaluate(readings(range(0, 131, 5)))["passed"])

    def test_sleep_then_resume_cannot_pass(self):
        data = readings(range(0, 131, 5)) + readings(range(300, 431, 5))
        self.assertFalse(evaluate(data)["passed"])

    def test_open_or_unknown_lid_cannot_pass(self):
        for closed in (False, None):
            self.assertFalse(evaluate(readings(range(0, 131, 5), closed))["passed"])

    def test_separate_closed_intervals_do_not_accumulate(self):
        data = readings(range(0, 66, 5))
        data += readings([70], closed=False)
        data += readings(range(75, 141, 5))
        self.assertFalse(evaluate(data)["passed"])

    def test_battery_cannot_pass(self):
        self.assertFalse(evaluate(readings(range(0, 131, 5), ac=False))["passed"])

    def test_no_samples_cannot_pass(self):
        self.assertFalse(evaluate([])["passed"])


if __name__ == "__main__":
    unittest.main()
