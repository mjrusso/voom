import importlib.util
import io
import json
from pathlib import Path
import tempfile
import struct
from unittest.mock import Mock, patch
import unittest

spec = importlib.util.spec_from_file_location("control", Path(__file__).with_name("agent.py"))
control = importlib.util.module_from_spec(spec)
spec.loader.exec_module(control)


class ProtocolTests(unittest.TestCase):
    def test_framing_and_protocol_rejection(self):
        stream = io.BytesIO()
        control.send(stream, "config", {"text": "line\nnext"})
        stream.seek(0)
        self.assertEqual(control.receive(stream, "config"), {"text": "line\nnext"})
        for data in (b"", b"{\n", b'{"version":2,"type":"config"}\n', b'{"version":1,"type":"wrong"}\n', b"x" * (control.MAX_MESSAGE + 1)):
            with self.subTest(size=len(data)), self.assertRaises(ValueError):
                control.receive(io.BytesIO(data), "config")
        with self.assertRaises(ValueError):
            control.send(io.BytesIO(), "report", "x" * control.MAX_MESSAGE)

    def test_darwin_native_sockaddr_and_connect_timeout(self):
        native = Mock()
        native.connect.return_value = 0
        connection = Mock()
        connection.fileno.return_value = 9
        with patch.object(control.sys, "platform", "darwin"), patch.object(control.ctypes, "CDLL", return_value=native), patch.object(control.socket, "socket", return_value=connection):
            self.assertIs(control.connect(), connection)
        native.connect.assert_called_once_with(9, struct.pack("=BBHII", 12, 40, 0, 49152, 2), 12)
        connection.settimeout.assert_called_with(20)
        native.connect.return_value = -1
        with patch.object(control.sys, "platform", "darwin"), patch.object(control.ctypes, "CDLL", return_value=native), patch.object(control.ctypes, "get_errno", return_value=control.errno.EINPROGRESS), patch.object(control.socket, "socket", return_value=connection), patch.object(control.select, "select", return_value=([], [], [])):
            with self.assertRaises(TimeoutError):
                control.connect()
        connection.close.assert_called_once()

    def test_configuration_replay_and_egress_removal(self):
        config = {"metadata": {"hostname": "vm"}, "mounts": [], "egress": {"mode": "explicit"}, "egressCA": "certificate"}
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary)
            for _ in range(2):
                control.publish_config(config, path)
                self.assertEqual(json.loads((path / "metadata.json").read_text()), config["metadata"])
                self.assertEqual((path / "egress-ca.pem").read_text(), "certificate")
            control.publish_config({"metadata": {}, "mounts": []}, path)
            self.assertFalse((path / "egress.json").exists())
            self.assertFalse((path / "egress-ca.pem").exists())


if __name__ == "__main__":
    unittest.main()
