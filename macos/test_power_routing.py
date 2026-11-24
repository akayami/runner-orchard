import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

import power_routing


class AdmissionTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        self.agent = power_routing.Routing.__new__(power_routing.Routing)
        self.agent.root = root
        self.agent.plist = root / "listener.plist"
        self.agent.domain = "gui/501"
        self.agent.service = "gui/501/runner"
        self.agent.stopped = root / "power-stopped"
        self.agent.last_state = self.agent.idle_since = None
        self.agent.next_check = 0
        self.loaded, self.ac, self.busy = True, True, False
        self.agent.remote = Mock(side_effect=lambda desired: {"busy": self.busy})
        def launch(action, *args, **kwargs):
            if action == "bootout":
                self.assertFalse(self.busy)
                self.loaded = False
            if action == "bootstrap":
                self.assertTrue(self.ac)
                self.loaded = True
            return Mock(returncode=0 if self.loaded else 1)
        for key, callback in (("on_ac", lambda: self.ac), ("local_busy", lambda: self.busy), ("launch", launch)):
            swap = patch.object(power_routing, key, callback)
            swap.start()
            self.addCleanup(swap.stop)

    def test_unplug_finish_idle_reconnect(self):
        self.agent.step(0)
        self.agent.remote.assert_called_with(True)
        self.ac, self.busy = False, True
        self.agent.step(2)
        self.agent.remote.assert_called_with(False)
        self.agent.step(100)
        self.assertTrue(self.loaded)
        self.busy = False
        self.agent.step(110)
        self.agent.step(132)
        self.assertFalse(self.loaded)
        self.agent.step(137)
        self.ac = True
        self.agent.step(138)
        self.assertTrue(self.loaded)
        self.agent.step(140)
        self.agent.remote.assert_called_with(True)

    def test_manual_drain_survives_replug(self):
        self.loaded = self.ac = False
        self.agent.stopped.touch()
        (self.agent.root / ".drained").touch()
        self.agent.step(0)
        self.ac = True
        self.agent.step(3)
        self.assertFalse(self.loaded)
        self.agent.remote.assert_called_with(False)
