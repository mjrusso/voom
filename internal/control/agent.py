import ctypes
import errno
import select
import json
import os
from pathlib import Path
import socket
import struct
import sys
import time

VERSION = 1
PORT = 49152
MAX_MESSAGE = 8 * 1024 * 1024


def send(stream, kind, payload=None):
    data = json.dumps({"version": VERSION, "type": kind, "payload": payload}, separators=(",", ":")).encode() + b"\n"
    if len(data) > MAX_MESSAGE:
        raise ValueError("control message too large")
    stream.write(data)
    stream.flush()


def receive(stream, kind):
    data = stream.readline(MAX_MESSAGE + 1)
    if not data or len(data) > MAX_MESSAGE or not data.endswith(b"\n"):
        raise ValueError("incomplete or oversized control message")
    message = json.loads(data)
    if not isinstance(message, dict) or type(message.get("version")) is not int or message.get("version") != VERSION or message.get("type") != kind:
        raise ValueError("unsupported control message")
    return message.get("payload")


def connect():
    if sys.platform == "darwin":
        # Darwin sockaddr_vm starts with length and family bytes, unlike Linux.
        libc = ctypes.CDLL(None, use_errno=True)
        connection = socket.socket(40, socket.SOCK_STREAM)
        connection.setblocking(False)
        address = struct.pack("=BBHII", 12, 40, 0, PORT, 2)
        try:
            if libc.connect(connection.fileno(), address, len(address)) != 0:
                error = ctypes.get_errno()
                if error != errno.EINPROGRESS:
                    raise OSError(error, "AF_VSOCK connect")
                if not select.select([], [connection], [], 10)[1]:
                    raise TimeoutError("AF_VSOCK connect timed out")
                error = connection.getsockopt(socket.SOL_SOCKET, socket.SO_ERROR)
                if error:
                    raise OSError(error, "AF_VSOCK connect")
        except BaseException:
            connection.close()
            raise
    else:
        path = Path("/sys/firmware/qemu_fw_cfg/by_name/opt/voom/control-port/raw")
        port = int(path.read_bytes().strip(b"\0")) if path.exists() else PORT
        connection = socket.socket(socket.AF_VSOCK, socket.SOCK_STREAM)
        connection.settimeout(10)
        try:
            connection.connect((2, port))
        except BaseException:
            connection.close()
            raise
    connection.settimeout(20)
    return connection


def atomic_write(path, data):
    path = Path(path)
    if path.is_file() and path.read_text() == data:
        return
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(data)
    temporary.chmod(0o644)
    temporary.replace(path)


def publish_config(config, directory):
    directory.mkdir(parents=True, exist_ok=True)
    for name in ("metadata", "mounts"):
        atomic_write(directory / (name + ".json"), json.dumps(config[name]))
    ca = config.get("egressCA")
    if ca:
        atomic_write(directory / "egress-ca.pem", ca)
    egress = config.get("egress")
    if egress:
        atomic_write(directory / "egress.json", json.dumps(egress))
    else:
        (directory / "egress.json").unlink(missing_ok=True)
    if not ca:
        (directory / "egress-ca.pem").unlink(missing_ok=True)


def notify_ready():
    address = os.environ.get("NOTIFY_SOCKET")
    if address:
        if address.startswith("@"):
            address = "\0" + address[1:]
        with socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM) as notification:
            notification.sendto(b"READY=1", address)


def run(adapter):
    previous = None
    while True:
        try:
            with connect() as connection, connection.makefile("rwb") as stream:
                send(stream, "hello")
                while True:
                    config = receive(stream, "config")
                    if not isinstance(config, dict) or not isinstance(config.get("metadata"), dict) or not isinstance(config.get("mounts"), list):
                        raise ValueError("invalid control configuration")
                    publish_config(config, adapter.CONTROL)
                    errors = adapter.apply_config(config)
                    send(stream, "report", {"ports": adapter.report_ports(), "errors": errors})
                    notify_ready()
                    previous = None
                    time.sleep(2)
        except (OSError, ValueError, RuntimeError) as error:
            message = str(error)
            if message != previous:
                print(f"vsock control: {message}", file=sys.stderr, flush=True)
                previous = message
            time.sleep(2)
