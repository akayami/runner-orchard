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
        with patch("builtins.input", side_effect=answer), patch.object(setup, "xcode_path", return_value=RUNNER["developer_dir"]), patch.object(setup, "existing_workers", return_value=[]):
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
        with patch("builtins.input", side_effect=answer), patch.object(setup, "xcode_path", return_value=RUNNER["developer_dir"]), patch.object(setup, "existing_workers", return_value=[]):
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

    def test_discovers_legacy_workers_without_modifying_registration(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp)
            legacy = home / ".local/share/github-runners"
            first = legacy / "custom-one"
            second = legacy / "custom-two"
            for worker in (first, second):
                worker.mkdir(parents=True)
                (worker / ".runner").write_bytes(b"registration retained verbatim")
            (legacy / "not-registered").mkdir()
            with patch.object(setup.Path, "home", return_value=home):
                self.assertEqual(setup.existing_workers(), [first, second])
            for worker in (first, second):
                self.assertEqual((worker / ".runner").read_bytes(), b"registration retained verbatim")

    def test_wizard_allows_coexistence_in_both_modes(self):
        for prefix in (["1", "example", "mac-worker", "apple-builds", "", RUNNER["developer_dir"], ""],
                       ["2", "http://orchard.example.test", "mac-worker", RUNNER["developer_dir"]]):
            with self.subTest(mode=prefix[0]):
                prompts = []
                answers = iter([*prefix, "y"])
                def answer(prompt):
                    prompts.append(prompt)
                    return next(answers)
                with patch("builtins.input", side_effect=answer), patch.object(setup, "xcode_path", return_value=RUNNER["developer_dir"]), patch.object(setup, "existing_workers", return_value=[Path("/example/custom-worker")]):
                    cfg = setup.interactive()
                self.assertTrue(cfg["allow_existing_workers"])
                self.assertFalse(cfg["power_management"])
                self.assertTrue(prompts[-1].startswith("Install Orchard alongside"))

    def test_wizard_declining_coexistence_cancels(self):
        with patch("builtins.input", side_effect=["2", "http://orchard.example.test", "mac-worker", RUNNER["developer_dir"], ""]), patch.object(setup, "xcode_path", return_value=RUNNER["developer_dir"]), patch.object(setup, "existing_workers", return_value=[Path("/example/custom-worker")]):
            with self.assertRaisesRegex(RuntimeError, "cancelled"):
                setup.interactive()

    def test_json_coexistence_is_explicit_and_boolean(self):
        for mode in ("standalone", "orchard"):
            raw = {"mode": mode, **({"runner": RUNNER} if mode == "standalone" else {"console_url": "http://orchard.example.test"})}
            with self.subTest(mode=mode), patch.object(setup, "existing_workers", return_value=[Path("/example/custom-worker")]), patch("builtins.input", side_effect=AssertionError("prompted")):
                cfg = setup.normalize(raw)
                self.assertFalse(cfg["allow_existing_workers"])
                with self.assertRaisesRegex(RuntimeError, "allow_existing_workers=true"):
                    setup.check_existing_workers(cfg)
                setup.check_existing_workers(setup.normalize(dict(raw, allow_existing_workers=True)))
                for invalid in ("true", 1, None):
                    with self.assertRaises(ValueError):
                        setup.normalize(dict(raw, allow_existing_workers=invalid))

    def test_coexistence_install_leaves_custom_worker_untouched(self):
        with tempfile.TemporaryDirectory() as tmp:
            worker = Path(tmp) / "custom-worker"
            worker.mkdir()
            registration = worker / ".runner"
            registration.write_text("original registration")
            cfg = setup.normalize({"mode": "standalone", "runner": RUNNER, "allow_existing_workers": True})
            with patch.object(setup.platform, "system", return_value="Darwin"), patch.object(setup.platform, "machine", return_value="arm64"), patch.object(setup.os, "getuid", return_value=501), patch.object(setup, "existing_workers", return_value=[worker]), patch.object(setup, "tools"), patch.object(setup.subprocess, "run") as command, patch.object(setup.runner, "install") as install, patch.object(setup.health, "install") as health:
                command.return_value.returncode = 0
                setup.install(cfg, {}, noninteractive=True)
            install.assert_called_once_with(RUNNER, enrollment=None)
            health.assert_called_once_with(require_power=False)
            self.assertEqual(registration.read_text(), "original registration")
            self.assertEqual(command.call_args_list[0].args[0], ["gh", "auth", "status", "--hostname", "github.com"])
            self.assertEqual(command.call_count, 1)

    def test_existing_orchard_registration_cannot_be_bypassed(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            (base / ".runner").write_text("existing orchard worker")
            cfg = setup.normalize({"mode": "orchard", "console_url": "http://orchard.example.test", "allow_existing_workers": True})
            with patch.object(setup.platform, "system", return_value="Darwin"), patch.object(setup.platform, "machine", return_value="arm64"), patch.object(setup.os, "getuid", return_value=501), patch.object(setup.runner, "BASE", base), patch.object(setup, "tools") as tools:
                with self.assertRaisesRegex(RuntimeError, "already registered"):
                    setup.install(cfg, {}, noninteractive=True)
            tools.assert_not_called()
            self.assertEqual((base / ".runner").read_text(), "existing orchard worker")

    def test_coexistence_refuses_second_power_setup_before_installing_tools(self):
        cfg = setup.normalize({"mode": "standalone", "runner": RUNNER, "allow_existing_workers": True, "power_management": True})
        with patch.object(setup.platform, "system", return_value="Darwin"), patch.object(setup.platform, "machine", return_value="arm64"), patch.object(setup.os, "getuid", return_value=501), patch.object(setup, "existing_workers", return_value=[Path("/example/custom-worker")]), patch.object(setup, "tools") as tools:
            with self.assertRaisesRegex(RuntimeError, "power_management=false"):
                setup.install(cfg, {}, noninteractive=True)
        tools.assert_not_called()


if __name__ == "__main__":
    unittest.main()
