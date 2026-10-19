import json
from pathlib import Path
import re
import subprocess
import sys
from datetime import datetime, timezone
import voom_control

CONTROL = Path("/run/voom")


def report_ports():
    result = subprocess.run(["ss", "-tlnpH"], check=True, capture_output=True, text=True, timeout=10)
    listeners = []
    for line in result.stdout.splitlines():
        fields = line.split()
        if len(fields) < 4 or fields[0] != "LISTEN":
            raise ValueError("invalid ss output")
        addr, port = fields[3].rsplit(":", 1)
        entry = {"proto": "tcp", "addr": addr.strip("[]").split("%", 1)[0], "port": int(port)}
        process = re.search(r'users:\(\("([^"\n]*)",pid=(\d+)', line)
        if process:
            entry.update(process=process[1], pid=int(process[2]))
        listeners.append(entry)
    return {"schemaVersion": 1, "generatedAt": datetime.now(timezone.utc).isoformat(), "listeners": listeners}


def apply_config(config):
    errors = []
    for share in config["mounts"]:
        try:
            target = Path(share["guestPath"])
            if not target.is_absolute() or target == Path("/") or target.resolve().is_relative_to(CONTROL) or CONTROL.is_relative_to(target.resolve()):
                raise ValueError("share overlaps reserved storage")
            if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", share["tag"]) or type(share["readonly"]) is not bool:
                raise ValueError("invalid share declaration")
            result = subprocess.run(["findmnt", "-J", "-M", str(target), "-o", "SOURCE,FSTYPE,OPTIONS"], capture_output=True, text=True, timeout=10)
            if result.returncode == 0:
                mount = json.loads(result.stdout)["filesystems"][0]
                if mount["source"] != share["tag"] or mount["fstype"] != "virtiofs" or (share["readonly"] and "ro" not in mount["options"].split(",")):
                    raise RuntimeError(f"conflicting mount at {target}")
            elif result.returncode == 1:
                target.mkdir(parents=True, exist_ok=True)
                subprocess.run(["mount", "-t", "virtiofs", "-o", "ro" if share["readonly"] else "rw", share["tag"], str(target)], check=True, capture_output=True, text=True, timeout=15)
            else:
                raise RuntimeError(result.stderr)
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
            errors.append(f"share {share.get('tag')}: {error}")
    hostname = config["metadata"].get("hostname", "")
    if re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,62}", hostname):
        subprocess.run(["hostname", hostname], check=True, capture_output=True, timeout=10)
    return errors


if __name__ == "__main__":
    voom_control.run(sys.modules[__name__])
