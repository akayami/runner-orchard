import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import runner


CONFIG = {"organization": "example", "name": "mac-01", "runner_group": "apple-builds",
          "labels": ["apple-build"], "developer_dir": "/Applications/Xcode.app/Contents/Developer"}


class RunnerLifecycleTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name) / "runner"
        self.base.mkdir()
        self.patch = patch.object(runner, "BASE", self.base)
        self.patch.start()
        self.addCleanup(self.patch.stop)
        (self.base / ".runner").write_text(json.dumps({"agentId": 42, "agentName": "mac-01",
                                                      "gitHubUrl": "https://github.com/example"}))

    def test_drain_waits_for_busy_job_and_repeated_idle_observations(self):
        events = []
        responses = iter([True, False, False, False])

        def api(endpoint, method="GET", payload=None):
            events.append((method, endpoint))
            if endpoint.endswith("/labels"):
                return {"labels": [{"name": "apple-build"}]}
            if method == "DELETE":
                return
            return {"busy": next(responses)}

        with (patch.object(runner, "github", side_effect=api),
              patch.object(runner.time, "sleep"),
              patch.object(runner, "service") as service):
            runner.drain(CONFIG, 60)
        self.assertEqual(events[1], ("DELETE", "orgs/example/actions/runners/42/labels/apple-build"))
        self.assertEqual(len([e for e in events if e[1].endswith("/42")]), 4)
        service.assert_called_once_with(CONFIG, "stop")
        self.assertTrue((self.base / ".drained").exists())

    def test_drain_timeout_does_not_interrupt_job(self):
        def api(endpoint, method="GET", payload=None):
            return {"labels": []} if endpoint.endswith("/labels") else {"busy": True}
        with (patch.object(runner, "github", side_effect=api),
              patch.object(runner.time, "monotonic", side_effect=[0, 31]),
              patch.object(runner, "service") as service):
            with self.assertRaisesRegex(RuntimeError, "timed out"):
                runner.drain(CONFIG, 30)
        service.assert_not_called()
        self.assertTrue((self.base / ".drained").exists())

    def test_group_policy_mismatch_does_not_widen_access(self):
        with (patch.object(runner, "github", return_value={"private": True, "id": 1}) as api,
              patch.object(runner, "pages", side_effect=[[
                  {"name": "apple-builds", "id": 3, "visibility": "all", "allows_public_repositories": False}
              ], [{"id": 1}]])):
            with self.assertRaisesRegex(RuntimeError, "policy differs"):
                runner.provision_group(CONFIG, ["app"])
        self.assertTrue(all(len(call.args) == 1 for call in api.call_args_list))

    def test_public_repo_refused_before_group_creation(self):
        with patch.object(runner, "github", return_value={"private": False}) as api:
            with self.assertRaisesRegex(RuntimeError, "must be private"):
                runner.provision_group(CONFIG, ["app"])
        api.assert_called_once_with("repos/example/app")

    def test_cleanup_does_not_follow_symlinked_parent(self):
        outside = Path(self.temp.name) / "outside"
        outside.mkdir()
        (outside / "_temp").mkdir()
        secret = outside / "_temp" / "keep.txt"
        secret.write_text("keep")
        (self.base / "_work").symlink_to(outside, target_is_directory=True)
        (self.base / ".drained").touch()
        with (patch.object(runner, "require_idle"), patch.object(runner, "service_loaded", return_value=False),
              patch.object(runner.time, "time", return_value=10**12)):
            runner.cleanup(CONFIG, 1, True)
        self.assertTrue(secret.exists())

    def test_different_machine_registration_is_refused(self):
        with self.assertRaisesRegex(RuntimeError, "differs"):
            runner.registration(dict(CONFIG, name="other-mac"))

    def test_resume_excludes_builtin_labels_and_clears_marker(self):
        (self.base / ".drained").touch()
        config = dict(CONFIG, labels=["apple-build", "ios", "macos", "ARM64"])
        with patch.object(runner, "github") as api, patch.object(runner, "service") as service:
            runner.resume(config)
        api.assert_called_once_with("orgs/example/actions/runners/42/labels", "POST",
                                    {"labels": ["apple-build", "ios"]})
        service.assert_called_once_with(config, "start")
        self.assertFalse((self.base / ".drained").exists())

    def test_service_marker_supports_actual_github_plist_path(self):
        home = Path(self.temp.name) / "home"
        plist = home / "Library/LaunchAgents/actions.runner.example.mac-01.plist"
        (self.base / ".service").write_text(str(plist) + "\n")
        with patch.object(runner.Path, "home", return_value=home):
            self.assertEqual(runner.service_path(), plist)

    def test_service_marker_rejects_unexpected_path(self):
        (self.base / ".service").write_text("/tmp/other.plist\n")
        with self.assertRaisesRegex(RuntimeError, "Unexpected"):
            runner.service_path()


if __name__ == "__main__":
    unittest.main()
