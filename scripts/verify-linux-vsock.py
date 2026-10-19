#!/usr/bin/env python3
import argparse
import errno
import fcntl
import json
import os
from pathlib import Path
import shlex
import signal
import socketserver
import struct
import subprocess
import tempfile
import threading
import time
import urllib.request


class Proxy(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.settimeout(3)
        try:
            if self.request.recv(16384):
                self.request.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: 9\r\nConnection: close\r\n\r\nproxy-ok\n")
        except OSError:
            pass


class ProxyServer(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


class Acceptance:
    def __init__(self, args, root):
        self.args, self.root = args, root
        self.env = {key: value for key, value in os.environ.items() if not key.startswith("VOOM_")}
        for name in ("CONFIG", "STATE", "CACHE", "RUNTIME"):
            self.env[f"VOOM_{name}_DIR"] = str(root / name.lower())
        self.results = {}
        self.sequence = 0
        self.identity = root / "identity"
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(self.identity)], check=True)

    def command(self, *args, check=True):
        self.sequence += 1
        result = subprocess.run([str(self.args.voom), *args], env=self.env, text=True, capture_output=True, timeout=self.args.timeout)
        (self.root / f"{self.sequence:03d}.json").write_text(json.dumps({"executable": str(self.args.voom), "args": args, "code": result.returncode, "stdout": result.stdout, "stderr": result.stderr}, indent=2))
        if check and result.returncode:
            raise RuntimeError(f"{' '.join(args)}: {result.stderr.strip()}")
        return result

    def json(self, *args):
        return json.loads(self.command(*args, "--output", "json").stdout)

    def ssh(self, vm, script):
        return self.command("ssh", vm, "--", "sh -c " + shlex.quote("set -eu\n" + script)).stdout

    def wait(self, name, check):
        deadline = time.monotonic() + self.args.timeout
        while time.monotonic() < deadline:
            try:
                result = check()
                if result:
                    self.results[name] = True
                    print(name + ": passed", flush=True)
                    return result
            except (RuntimeError, ValueError, subprocess.TimeoutExpired):
                pass
            time.sleep(1)
        raise TimeoutError(name)

    def endpoint(self, vm):
        info = self.json("info", vm)
        return self.root / "runtime" / "vms" / info["vm"]["id"]

    def ports(self, vm):
        return self.json("guest", "ports", vm)

    def forward(self, vm, port):
        return next((row for row in self.json("forward", "list", vm) if row["guestPort"] == port and row["installed"]), None)

    def http(self, vm, port):
        self.ssh(vm, f"nohup python3 -m http.server {port} --bind 0.0.0.0 --directory /mnt/rw >/tmp/voom-http.log 2>&1 </dev/null & echo $! >/tmp/voom-http.pid")
        row = self.wait(vm + "-forward-" + str(port), lambda: self.forward(vm, port))
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(f"http://127.0.0.1:{row['hostPort']}/marker", timeout=10) as response:
            if response.read() != b"from-host\n":
                raise RuntimeError("forwarded traffic mismatch")

    def shares(self):
        self.ssh("control-a", "sudo sh -c 'set -eu; test \"$(cat /mnt/rw/marker)\" = from-host; echo from-guest > /mnt/rw/result; if echo bad > /mnt/ro/result 2>/dev/null; then exit 1; fi; ! mount | grep voom-control'")
        self.ssh("control-a", f"sudo -n setpriv --reuid={os.getuid()} --regid={os.getgid()} --clear-groups sh -c 'echo from-user > /mnt/rw/user-result'")
        return (self.root / "rw" / "result").read_text() == "from-guest\n" and (self.root / "rw" / "user-result").read_text() == "from-user\n"

    def claim_cid(self, cid):
        with open("/dev/vhost-vsock", "rb+", buffering=0) as descriptor:
            fcntl.ioctl(descriptor, 0x4008AF60, struct.pack("=Q", cid))

    def run(self):
        for mode in ("rw", "ro"):
            directory = self.root / mode
            directory.mkdir()
            (directory / "marker").write_text("from-host\n")
        self.command("image", "import", "control-image", str(self.args.disk), "--arch", "x86_64-linux", "--format", "qcow2", "--ssh-user", self.args.ssh_user, "--ssh-identity", str(self.identity), "--install-guest-helpers")
        self.command("create", "control-a", "--image", "control-image", "--driver", "qemu")
        for mode in ("rw", "ro"):
            flags = ("--readonly",) if mode == "ro" else ()
            self.command("share", "add", "control-a", mode, str(self.root / mode), "/mnt/" + mode, *flags)
        self.command("forward", "auto", "enable", "control-a")
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(self.root / "ca.key"), "-out", str(self.root / "ca.pem"), "-days", "1", "-subj", "/CN=Voom disposable test"], check=True, capture_output=True)
        certificate = self.root / "ca.pem"
        certificate.write_text(certificate.read_text() * 1024)
        self.command("config", "egress", "set", "control-a", "--backend-socket", str(self.root / "proxy.sock"), "--ca-cert", str(self.root / "ca.pem"))
        self.command("start", "control-a")
        self.wait("initial-report", lambda: self.ports("control-a"))
        self.wait("read-write-read-only-no-control-share", self.shares)
        runtime = self.endpoint("control-a")
        endpoint = json.loads((runtime / "control-endpoint.json").read_text())
        try:
            self.claim_cid(endpoint["cid"])
        except OSError as error:
            if error.errno != errno.EADDRINUSE:
                raise
        else:
            raise RuntimeError("running guest CID was not reserved")
        self.http("control-a", 18080)
        self.ssh("control-a", "sudo systemctl stop voom-guest")
        self.wait("disconnected-report-and-forwards-removed", lambda: self.command("guest", "ports", "control-a", check=False).returncode != 0 and not self.forward("control-a", 18080))
        self.ssh("control-a", "sudo systemctl start voom-guest")
        self.wait("agent-reconnect-report", lambda: self.ports("control-a"))
        self.wait("agent-reconnect-forward", lambda: self.forward("control-a", 18080))
        self.ssh("control-a", "kill $(cat /tmp/voom-http.pid)")
        self.wait("listener-close-removes-forward", lambda: not self.forward("control-a", 18080))
        self.command("stop", "control-a")
        self.claim_cid(endpoint["cid"])
        self.results["stopped-cid-released"] = True
        if list(runtime.glob("*.sock")) or list(runtime.glob("*.process.json")) or (runtime / "control-endpoint.json").exists():
            raise RuntimeError("stopped runtime artifacts remain")
        self.command("clone", "control-a", "control-b")
        self.command("forward", "auto", "enable", "control-b")
        self.command("start", "control-a")
        self.command("start", "control-b")
        self.wait("cold-restart-report", lambda: self.ports("control-a"))
        self.wait("cold-restart-shares", self.shares)
        self.http("control-a", 18080)
        self.wait("second-vm-report", lambda: self.ports("control-b"))
        other_runtime = self.endpoint("control-b")
        endpoints = [json.loads((path / "control-endpoint.json").read_text()) for path in (runtime, other_runtime)]
        (self.root / "endpoints.json").write_text(json.dumps(endpoints, indent=2))
        if len({row["cid"] for row in endpoints}) != 2 or len({row["port"] for row in endpoints}) != 2:
            raise RuntimeError("VM endpoint collision")
        self.results["concurrent-cids-and-ports"] = True
        self.wait("vm-configuration-isolation", lambda: self.ssh("control-b", "test \"$(hostname)\" = control-b; ! mount | grep /mnt/rw; test ! -e /run/voom/egress.json; echo isolated").strip() == "isolated")
        self.wait("egress-ca-replayed", lambda: self.ssh("control-a", "cat /run/voom/egress-ca.pem") == (self.root / "ca.pem").read_text())
        code = "import urllib.request; op=urllib.request.build_opener(urllib.request.ProxyHandler({'http':'http://192.168.127.1:3128'})); print(op.open('http://voom.invalid/test',timeout=5).read().decode(),end='')"
        if self.ssh("control-a", "python3 -c " + shlex.quote(code)) != "proxy-ok\n":
            raise RuntimeError("egress proxy traffic mismatch")
        self.results["egress-proxy-traffic"] = True
        self.command("config", "egress", "disable", "control-a")
        self.wait("egress-disable-replayed", lambda: self.ssh("control-a", "test ! -e /run/voom/egress.json; test ! -e /run/voom/egress-ca.pem; echo disabled").strip() == "disabled")
        self.command("config", "egress", "enable", "control-a")
        self.wait("egress-enable-replayed", lambda: self.ssh("control-a", "cat /run/voom/egress-ca.pem") == (self.root / "ca.pem").read_text())
        self.ssh("control-b", "nohup python3 -m http.server 18181 --bind 0.0.0.0 >/tmp/voom-http.log 2>&1 </dev/null &")
        self.wait("second-vm-forward", lambda: self.forward("control-b", 18181))
        if self.forward("control-a", 18181) or self.forward("control-b", 18080):
            raise RuntimeError("cross-VM listener report")
        self.results["listener-isolation"] = True
        record = json.loads((other_runtime / "vm.process.json").read_text())
        os.kill(record["pid"], signal.SIGKILL)
        self.wait("driver-exit-cleans-helpers", lambda: not list(other_runtime.glob("*.process.json")) and not list(other_runtime.glob("*.sock")) and not (other_runtime / "control-endpoint.json").exists())
        self.claim_cid(endpoints[1]["cid"])
        self.results["driver-exit-releases-cid"] = True

    def cleanup(self):
        for row in self.json("list"):
            self.command("stop", row["name"])
            self.command("remove", row["name"], "--force")
        for row in self.json("image", "list"):
            self.command("image", "remove", row["name"], "--force")
        if list((self.root / "runtime").glob("vms/*/*.process.json")) or list((self.root / "runtime").glob("vms/*/*.sock")):
            raise RuntimeError("runtime cleanup incomplete")
        self.results["stop-remove-cleanup"] = True


def main():
    parser = argparse.ArgumentParser(description="Exercise Linux QEMU vsock using disposable Voom objects and a cloud-init qcow2 image.")
    parser.add_argument("--voom", type=Path, required=True)
    parser.add_argument("--disk", type=Path, required=True)
    parser.add_argument("--ssh-user", default="debian")
    parser.add_argument("--timeout", type=int, default=240)
    args = parser.parse_args()
    args.voom, args.disk = args.voom.resolve(), args.disk.resolve()
    root = Path(tempfile.mkdtemp(prefix="voom-vsock-", dir="/tmp"))
    print("Evidence: " + str(root), flush=True)
    acceptance = Acceptance(args, root)
    error = None
    with ProxyServer(str(root / "proxy.sock"), Proxy) as server:
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            acceptance.run()
        except Exception as failure:
            error = str(failure)
        finally:
            try:
                acceptance.cleanup()
            except Exception as failure:
                error = f"{error or ''}; cleanup: {failure}"
            server.shutdown()
    result = {"passed": error is None, "error": error, "checks": acceptance.results}
    (root / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result), flush=True)
    return 0 if error is None else 1


if __name__ == "__main__":
    raise SystemExit(main())
