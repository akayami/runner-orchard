import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import setup

RUNNER = {"organization": "example", "name": "mac-worker", "runner_group": "apple-builds",
          "labels": ["apple-build"], "developer_dir": "/Applications/Xcode.app/Contents/Developer"}


class SetupTests(unittest.TestCase):
    def test_mode_is_first_question_and_standalone_never_asks_console(self):
        prompts = []
        answers = iter(["1", "example", "mac-worker", "apple-builds", "", RUNNER["developer_dir"], "", "n"])
        def answer(prompt):
            prompts.append(prompt)
            return next(answers)
        with patch("builtins.input", side_effect=answer), patch.object(setup, "xcode_path", return_value=RUNNER["developer_dir"]):
            cfg = setup.interactive()
        self.assertTrue(prompts[0].startswith("Mode"))
        self.assertEqual(cfg["mode"], "standalone")
        self.assertFalse(any("console URL" in p for p in prompts))
        self.assertEqual(cfg["runner"]["organization"], "example")

    def test_orchard_questions_do_not_ask_for_org_group_or_labels(self):
        prompts = []
        answers = iter(["2", "http://orchard.example.test/", "mac-worker", RUNNER["developer_dir"], "n"])
        def answer(prompt):
            prompts.append(prompt)
            return next(answers)
        with patch("builtins.input", side_effect=answer), patch.object(setup, "xcode_path", return_value=RUNNER["developer_dir"]):
            cfg = setup.interactive()
        self.assertEqual(cfg["console_url"], "http://orchard.example.test")
        self.assertFalse(any("organization" in p or "Runner group" in p for p in prompts))

    def test_json_skips_questions_and_configure_only_has_no_side_effects(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            config = root / "setup.json"
            config.write_text(json.dumps({"mode": "standalone", "runner": RUNNER}))
            with patch("sys.argv", ["setup.py", "--config", str(config), "--configure-only", "--output-dir", str(root / "output")]), patch("builtins.input", side_effect=AssertionError("prompted")), patch.object(setup, "install") as install:
                self.assertEqual(setup.main(), 0)
            install.assert_not_called()
            saved = root / "output/local.json"
            self.assertEqual(json.loads(saved.read_text()), RUNNER)
            self.assertEqual(saved.stat().st_mode & 0o777, 0o600)

    def test_missing_json_fields_do_not_fall_back_to_prompts(self):
        with self.assertRaises(ValueError):
            setup.normalize({"mode": "standalone", "runner": {"name": "incomplete"}})
        with self.assertRaises(ValueError):
            setup.normalize({"mode": "orchard"})

    def test_orchard_json_uses_console_for_org_and_does_not_prompt(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            config = root / "setup.json"
            config.write_text(json.dumps({"mode": "orchard", "console_url": "http://orchard.example.test", "name": "mac-worker"}))
            with patch("sys.argv", ["setup.py", str(config), "--configure-only", "--output-dir", str(root / "output")]), patch("builtins.input", side_effect=AssertionError("prompted")), patch.object(setup, "install") as install:
                self.assertEqual(setup.main(), 0)
            install.assert_not_called()
            saved = json.loads((root / "output/local.setup.json").read_text())
            self.assertEqual(saved["mode"], "orchard")
            self.assertNotIn("runner", saved)
            self.assertFalse((root / "output/local.json").exists())

    def test_enrollment_handoff_contains_only_approved_config(self):
        cfg = setup.normalize({"mode": "orchard", "console_url": "https://orchard.example.test", "name": "mac-worker"})
        response = {"status": "ready", "runner": RUNNER, "registration_token": "synthetic-registration", "package": {}}
        with patch.object(setup, "api", side_effect=[{"id": "a" * 24}, {"status": "pending"}, response]) as api, patch.object(setup.time, "sleep"):
            self.assertEqual(setup.enroll(cfg), response)
        self.assertEqual(api.call_count, 3)
        self.assertEqual(api.call_args.args[0], "https://orchard.example.test/api/enrollment/" + "a" * 24 + "/claim")
        self.assertEqual(len(api.call_args.args[2]), 64)

    def test_legacy_config_and_url_validation(self):
        self.assertEqual(setup.normalize(RUNNER)["mode"], "standalone")
        for value in ("ftp://example.test", "http://user:password@example.test", "http://example.test/api/telemetry", "http://example.test?token=no", "http://example.test\n"):
            with self.assertRaises(ValueError):
                setup.console_url(value)

    def test_only_missing_tools_are_installed(self):
        cfg = setup.normalize({"mode": "standalone", "runner": RUNNER, "install_tools": True})
        with patch.object(setup.shutil, "which", side_effect=lambda command: None if command == "xcodegen" else "/example/" + command), patch.object(setup.subprocess, "run") as run:
            setup.tools(cfg, True)
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.args[0], ["brew", "install", "xcodegen"])


if __name__ == "__main__":
    unittest.main()
