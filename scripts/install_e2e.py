#!/usr/bin/env python3
"""Installer end-to-end test on a real Linux, macOS or Windows machine.

Starts a real controller (PATHLEDGER_DATABASE_URL must point at an empty
database), installs the agent with the platform's own installer as a
background service, waits until ICMP, TCP and traceroute results from it
arrive, then uninstalls it. Needs root/Administrator (installers do).

    python3 scripts/install_e2e.py --bin bin
"""
import argparse
import json
import os
import platform
import subprocess
import sys
import time
import urllib.request

PORT = 18090
BASE = f"http://127.0.0.1:{PORT}"
SYSTEM = platform.system()  # Linux, Darwin, Windows
EXE = ".exe" if SYSTEM == "Windows" else ""
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def call(method, path, key, body=None):
    req = urllib.request.Request(BASE + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read() or b"{}")


def installer(bin_dir, token):
    agent = os.path.join(bin_dir, "pathledger-agent" + EXE)
    if SYSTEM == "Windows":
        return ["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
                os.path.join(ROOT, "deploy", "windows", "install-agent.ps1"),
                "-ControllerUrl", BASE, "-EnrollToken", token, "-Binary", agent], None
    env = dict(os.environ, PATHLEDGER_ENROLL_TOKEN=token, PATHLEDGER_AGENT_BINARY=agent)
    return ["sh", os.path.join(ROOT, "deploy", "unix", "install-agent.sh"), BASE], env


def uninstall():
    if SYSTEM == "Windows":
        cmd = ["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
               os.path.join(ROOT, "deploy", "windows", "uninstall-agent.ps1"), "-Purge"]
    else:
        cmd = ["sh", os.path.join(ROOT, "deploy", "unix", "uninstall-agent.sh"), "--purge"]
    subprocess.run(cmd, check=False)


def agent_log():
    if SYSTEM == "Windows":
        p = os.path.join(os.environ.get("ProgramData", r"C:\ProgramData"), "PathLedger", "agent.log")
    elif SYSTEM == "Darwin":
        p = "/Library/Logs/PathLedger/agent.log"
    else:
        return subprocess.run(["journalctl", "-u", "pathledger-agent", "--no-pager", "-n", "40"],
                              capture_output=True, text=True).stdout
    try:
        return open(p, encoding="utf-8", errors="replace").read()[-4000:]
    except OSError as e:
        return str(e)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default="bin")
    ap.add_argument("--timeout", type=int, default=150)
    args = ap.parse_args()
    bin_dir = os.path.abspath(args.bin)
    ctl = os.path.join(bin_dir, "pathledger-controller" + EXE)

    env = dict(os.environ, PATHLEDGER_LISTEN_ADDR=f"127.0.0.1:{PORT}", PATHLEDGER_LOG_FORMAT="text")
    log = open("controller.log", "w")
    proc = subprocess.Popen([ctl, "serve"], env=env, stdout=log, stderr=subprocess.STDOUT)
    ok = False
    try:
        for _ in range(100):
            try:
                urllib.request.urlopen(BASE + "/readyz", timeout=2)
                break
            except OSError:
                time.sleep(0.3)
        out = subprocess.run([ctl, "tenant", "create", "-name", "Installer test"], env=env,
                             capture_output=True, text=True, check=True).stdout
        key = next(line.split()[-1] for line in out.splitlines() if line.strip().startswith("api key:"))
        site = call("POST", "/api/v1/sites", key, {"name": f"{SYSTEM} PC"})["id"]
        token = call("POST", f"/api/v1/sites/{site}/enrollment-tokens", key)["token"]
        for test in [
            {"kind": "icmp", "target": "127.0.0.1", "interval_seconds": 10, "timeout_millis": 1000},
            {"kind": "tcp", "target": f"127.0.0.1:{PORT}", "interval_seconds": 10, "timeout_millis": 1000},
            {"kind": "traceroute", "target": "127.0.0.1", "interval_seconds": 60, "timeout_millis": 1000,
             "params": {"max_hops": 3, "count": 1}},
        ]:
            call("POST", "/api/v1/tests", key, dict(test, site_id=site))

        cmd, ienv = installer(bin_dir, token)
        print("==> installing:", " ".join(c if c != token else "ple_***" for c in cmd), flush=True)
        subprocess.run(cmd, env=ienv, check=True)

        want = {"icmp", "tcp", "traceroute"}
        deadline = time.time() + args.timeout
        while time.time() < deadline:
            items = call("GET", f"/api/v1/results?site_id={site}&limit=200", key)["items"]
            good = {r["kind"] for r in items if r["success"]}
            if want <= good:
                ok = True
                break
            time.sleep(3)
        items = call("GET", f"/api/v1/results?site_id={site}&limit=200", key)["items"]
        latest = {}
        for r in items:
            latest.setdefault(r["kind"], r)
        for kind, r in sorted(latest.items()):
            print(f"   {kind:10} success={r['success']} latency_ms={r.get('latency_ms')} "
                  f"error={r.get('error', '')!r} hops={len(r.get('hops') or [])} details={r.get('details')}")
        agents = call("GET", "/api/v1/agents", key)["items"]
        print("   agent:", [(a["hostname"], a["os"], a["arch"], a["version"]) for a in agents])
        print("PASS" if ok else "FAIL: not every probe kind succeeded", flush=True)
        if not ok:
            print("--- agent log\n" + agent_log())
    finally:
        uninstall()
        proc.terminate()
        proc.wait(timeout=10)
        log.close()
        if not ok:
            print("--- controller log\n" + open("controller.log").read()[-4000:])
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
