#!/usr/bin/env python3
"""Deploy a Shelly script to multiple Gen2 devices via the HTTP RPC API."""

import json
import urllib.request
import urllib.error

SCRIPT_FILE = "shelly-rocker.js"
SCRIPT_NAME = "Rocker"

DEVICES = [
    "192.168.1.233",
    "192.168.1.106"
]


def rpc(ip, method, params=None):
    url = f"http://{ip}/rpc/{method}"
    data = json.dumps(params or {}).encode()
    req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read())


with open(SCRIPT_FILE) as f:
    code = f.read()

for ip in DEVICES:
    print(f"{ip}: ", end="", flush=True)
    try:
        # Remove existing script with the same name
        for s in rpc(ip, "Script.List").get("scripts", []):
            if s["name"] == SCRIPT_NAME:
                try:
                    rpc(ip, "Script.Stop", {"id": s["id"]})
                except Exception:
                    pass
                rpc(ip, "Script.Delete", {"id": s["id"]})

        # Create, upload, configure, start
        script_id = rpc(ip, "Script.Create", {"name": SCRIPT_NAME})["id"]
        rpc(ip, "Script.PutCode", {"id": script_id, "code": code, "append": False})
        rpc(ip, "Script.SetConfig", {"id": script_id, "config": {"name": SCRIPT_NAME, "enable": True}})
        rpc(ip, "Script.Start", {"id": script_id})

        print(f"deployed (script id {script_id})")
    except Exception as e:
        print(f"FAILED: {e}")
