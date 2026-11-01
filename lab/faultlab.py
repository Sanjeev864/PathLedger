#!/usr/bin/env python3
"""PathLedger fault-injection lab.

Builds a small internet out of Linux network namespaces, runs a real
controller and agent against it, injects faults at known places, and checks
that PathLedger blames the right segment. Results are written as a Markdown
table, which doubles as the accuracy evaluation for the project report.

    branch ── gw ──(ISP link)── isp ──(peering)── transit ── servers
   10.0.1.2  10.0.1.1  198.18.0.2/.1  198.51.100.1/.2  203.0.113.1  .10 .20
   (agent)   (LAN router)  AS64500 "Lab ISP"   AS64510 "Lab Transit"  AS64520

The agent reaches the controller over a separate management link, so faults
in the data path never stop results from arriving (in production a
fully-down site is reported as "offline" instead).

Requires root, Python 3.10+, pyroute2 (pip install pyroute2), nsenter, and
PostgreSQL reachable at PATHLEDGER_DATABASE_URL (an empty database).

    sudo -E python3 lab/faultlab.py --bin ./bin --out lab/results.md
"""

import argparse
import json
import os
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

from pyroute2 import IPRoute, NetNS, netns

PREFIX = "pl-"
NS = ["branch", "gw", "isp", "transit", "servers"]
CTL = "10.99.0.1"
CTL_PORT = 18080
SITE = "Lab branch"

ASN_TSV = (
    "198.18.0.0\t198.18.0.255\t64500\tIN\tLab ISP\n"
    "198.51.100.0\t198.51.100.255\t64510\tIN\tLab Transit\n"
    "203.0.113.0\t203.0.113.255\t64520\tIN\tLab Hosting\n"
)

# (namespace, address/prefix, interface) — links are pairs of these.
LINKS = [
    (("branch", "10.0.1.2/24", "b0"), ("gw", "10.0.1.1/24", "b1")),
    (("gw", "198.18.0.2/30", "g0"), ("isp", "198.18.0.1/30", "g1")),
    (("isp", "198.51.100.1/30", "i0"), ("transit", "198.51.100.2/30", "i1")),
    (("transit", "203.0.113.1/24", "s0"), ("servers", "203.0.113.10/24", "s1")),
]
EXTRA_ADDRS = [("servers", "s1", "203.0.113.20/24")]
ROUTES = {
    "branch": [("default", "10.0.1.1")],
    "gw": [("default", "198.18.0.1")],
    "isp": [("10.0.1.0/24", "198.18.0.2"), ("default", "198.51.100.2")],
    "transit": [("10.0.1.0/24", "198.51.100.1"), ("198.18.0.0/30", "198.51.100.1")],
    "servers": [("default", "203.0.113.1")],
}
ROUTERS = ["gw", "isp", "transit"]


def ns(name):
    return PREFIX + name


def nsexec(name, *cmd, **kw):
    return subprocess.Popen(["nsenter", f"--net=/var/run/netns/{ns(name)}", *cmd], **kw)


def nsrun(name, *cmd):
    subprocess.run(["nsenter", f"--net=/var/run/netns/{ns(name)}", *cmd], check=True)


def log(msg):
    print(f"[{time.strftime('%H:%M:%S')}] {msg}", flush=True)


# ---------------------------------------------------------------- topology

def teardown():
    for n in NS:
        try:
            netns.remove(ns(n))
        except Exception:
            pass
    with IPRoute() as ipr:
        idx = ipr.link_lookup(ifname="plmgmt0")
        if idx:
            ipr.link("del", index=idx[0])


def setup():
    teardown()
    for n in NS:
        netns.create(ns(n))
    for (a, b) in LINKS:
        (na, addr_a, if_a), (nb, addr_b, if_b) = a, b
        with NetNS(ns(na)) as x:
            x.link("add", ifname=if_a, kind="veth", peer={"ifname": if_b, "net_ns_fd": ns(nb)})
    for (a, b) in LINKS:
        for (n, addr, ifname) in (a, b):
            with NetNS(ns(n)) as x:
                idx = x.link_lookup(ifname=ifname)[0]
                ip, plen = addr.split("/")
                x.addr("add", index=idx, address=ip, prefixlen=int(plen))
                x.link("set", index=idx, state="up")
    for (n, ifname, addr) in EXTRA_ADDRS:
        with NetNS(ns(n)) as x:
            ip, plen = addr.split("/")
            x.addr("add", index=x.link_lookup(ifname=ifname)[0], address=ip, prefixlen=int(plen))
    for n in NS:
        with NetNS(ns(n)) as x:
            x.link("set", index=x.link_lookup(ifname="lo")[0], state="up")
    apply_routes()
    # Management link: root namespace <-> branch, for agent-to-controller traffic.
    with IPRoute() as ipr:
        ipr.link("add", ifname="plmgmt0", kind="veth", peer={"ifname": "plmgmt1", "net_ns_fd": ns("branch")})
        idx = ipr.link_lookup(ifname="plmgmt0")[0]
        ipr.addr("add", index=idx, address=CTL, prefixlen=30)
        ipr.link("set", index=idx, state="up")
    with NetNS(ns("branch")) as x:
        idx = x.link_lookup(ifname="plmgmt1")[0]
        x.addr("add", index=idx, address="10.99.0.2", prefixlen=30)
        x.link("set", index=idx, state="up")
    for n in ROUTERS:
        nsrun(n, "sh", "-c", "echo 1 > /proc/sys/net/ipv4/ip_forward; echo 0 > /proc/sys/net/ipv4/icmp_ratelimit")
    nsrun("branch", "sh", "-c", "echo '0 2147483647' > /proc/sys/net/ipv4/ping_group_range")


def apply_routes():
    """(Re)installs static routes. Linux flushes routes through an interface
    when it is taken down, so every repair re-applies them, as a router's
    configuration would on link recovery."""
    for n in NS:
        with NetNS(ns(n)) as x:
            for dst, gw in ROUTES.get(n, []):
                x.route("replace", dst="0.0.0.0/0" if dst == "default" else dst, gateway=gw)


def set_link(n, ifname, up):
    with NetNS(ns(n)) as x:
        x.link("set", index=x.link_lookup(ifname=ifname)[0], state="up" if up else "down")
    if up:
        time.sleep(0.5)
        apply_routes()


# ---------------------------------------------------------------- processes

class Procs:
    def __init__(self):
        self.p = {}

    def start(self, key, popen):
        self.p[key] = popen

    def stop(self, key):
        p = self.p.pop(key, None)
        if p and p.poll() is None:
            p.send_signal(signal.SIGTERM)
            try:
                p.wait(10)
            except subprocess.TimeoutExpired:
                p.kill()

    def stop_all(self):
        for k in list(self.p):
            self.stop(k)


def start_server(procs, ip):
    procs.start(f"web-{ip}", nsexec("servers", sys.executable, "-m", "http.server", "--bind", ip, "8080",
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))


RELAY = r"""
import socket, sys, threading
src = ("127.0.0.1", int(sys.argv[1])); dst = (sys.argv[2], int(sys.argv[3]))
def pipe(a, b):
    try:
        while (d := a.recv(65536)):
            b.sendall(d)
    except OSError:
        pass
    finally:
        for s in (a, b):
            try: s.shutdown(socket.SHUT_RDWR)
            except OSError: pass
ls = socket.socket(); ls.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); ls.bind(src); ls.listen(64)
while True:
    c, _ = ls.accept()
    u = socket.create_connection(dst)
    threading.Thread(target=pipe, args=(c, u), daemon=True).start()
    threading.Thread(target=pipe, args=(u, c), daemon=True).start()
"""


# ---------------------------------------------------------------- API

class API:
    def __init__(self, key):
        self.base = f"http://{CTL}:{CTL_PORT}"
        self.key = key

    def call(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Authorization", "Bearer " + self.key)
        if data:
            req.add_header("Content-Type", "application/json")
        with urllib.request.urlopen(req, timeout=10) as r:
            raw = r.read()
            return json.loads(raw) if raw else None


def wait(cond, timeout, step=2.0):
    end = time.time() + timeout
    while time.time() < end:
        v = cond()
        if v:
            return v
        time.sleep(step)
    return None


# ---------------------------------------------------------------- scenarios

def scenarios(procs):
    return [
        ("LAN router unreachable", "local",
         lambda: set_link("gw", "b1", False), lambda: set_link("gw", "b1", True)),
        ("ISP link down", "isp",
         lambda: set_link("gw", "g0", False), lambda: set_link("gw", "g0", True)),
        ("Transit network fails", "upstream",
         lambda: set_link("transit", "s0", False), lambda: set_link("transit", "s0", True)),
        ("One application down", "app",
         lambda: procs.stop("web-203.0.113.20"), lambda: start_server(procs, "203.0.113.20")),
    ]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default="./bin")
    ap.add_argument("--out", default="lab/results.md")
    ap.add_argument("--timeout", type=int, default=180, help="seconds to wait for each verdict")
    args = ap.parse_args()
    dsn = os.environ.get("PATHLEDGER_DATABASE_URL")
    if not dsn:
        sys.exit("set PATHLEDGER_DATABASE_URL to an empty database")
    if os.geteuid() != 0:
        sys.exit("run as root")

    work = tempfile.mkdtemp(prefix="pathledger-lab-")
    procs = Procs()
    rows, failures = [], 0
    try:
        log("building topology")
        setup()
        start_server(procs, "203.0.113.10")
        start_server(procs, "203.0.113.20")

        asn = os.path.join(work, "asn.tsv")
        with open(asn, "w") as f:
            f.write(ASN_TSV)
        env = dict(os.environ, PATHLEDGER_LISTEN_ADDR=f"{CTL}:{CTL_PORT}", PATHLEDGER_LOG_FORMAT="text",
                   PATHLEDGER_ASN_DB=asn, PATHLEDGER_ANALYTICS_WINDOW="40s", PATHLEDGER_ANALYTICS_INTERVAL="5s")
        ctl_log = open(os.path.join(work, "controller.log"), "w")
        procs.start("controller", subprocess.Popen([os.path.join(args.bin, "pathledger-controller"), "serve"],
                                                   env=env, stdout=ctl_log, stderr=subprocess.STDOUT))
        if not wait(lambda: _ready(), 30, 0.5):
            raise RuntimeError("controller did not become ready")

        out = subprocess.run([os.path.join(args.bin, "pathledger-controller"), "tenant", "create", "-name", "Lab"],
                             env=env, capture_output=True, text=True, check=True).stdout
        key = next(l.split()[-1] for l in out.splitlines() if "api key:" in l)
        api = API(key)
        site = api.call("POST", "/api/v1/sites", {"name": SITE, "location": "namespaces"})
        token = api.call("POST", f"/api/v1/sites/{site['id']}/enrollment-tokens")["token"]
        for t in [
            {"kind": "tcp", "target": "203.0.113.10:8080", "interval_seconds": 10, "timeout_millis": 1500},
            {"kind": "icmp", "target": "203.0.113.10", "interval_seconds": 10, "timeout_millis": 1000, "params": {"count": 3}},
            {"kind": "http", "target": "http://203.0.113.20:8080/", "interval_seconds": 10, "timeout_millis": 1500},
            {"kind": "traceroute", "target": "203.0.113.20", "interval_seconds": 60, "timeout_millis": 300,
             "params": {"max_hops": 8, "count": 2}},
        ]:
            api.call("POST", "/api/v1/tests", t)

        procs.start("relay", nsexec("branch", sys.executable, "-c", RELAY, str(CTL_PORT), CTL, str(CTL_PORT)))
        time.sleep(0.5)
        agent_env = dict(os.environ, PATHLEDGER_ENROLL_TOKEN=token)
        agent_log = open(os.path.join(work, "agent.log"), "w")
        procs.start("agent", nsexec("branch", os.path.join(args.bin, "pathledger-agent"),
                                    "-controller", f"http://127.0.0.1:{CTL_PORT}", "-state-dir", os.path.join(work, "agent"),
                                    "-log-format", "text", env=agent_env, stdout=agent_log, stderr=subprocess.STDOUT))

        log("warming up: waiting for a successful traceroute and healthy results")
        def healthy():
            res = api.call("GET", f"/api/v1/results?site_id={site['id']}&limit=200")["items"]
            kinds = {r["kind"] for r in res if r["success"]}
            path_ok = any(r["kind"] == "traceroute" and r["success"] for r in res)
            return kinds >= {"tcp", "icmp", "http"} and path_ok and not open_event(api, site["id"])
        if not wait(healthy, 150, 3):
            raise RuntimeError("lab never became healthy; see logs in " + work)
        path = next(r for r in api.call("GET", f"/api/v1/results?site_id={site['id']}&limit=200")["items"] if r["kind"] == "traceroute")
        log("baseline path: " + " → ".join(f"{h.get('addr') or '*'}({h.get('scope', '')}{' AS' + str(h['asn']) if h.get('asn') else ''})" for h in path["hops"]))

        for name, want, inject, revert in scenarios(procs):
            log(f"scenario: {name} (expect {want})")
            t0 = time.time()
            inject()
            first = wait(lambda: open_event(api, site["id"]), args.timeout, 2)
            t_first = time.time() - t0 if first else None
            correct = wait(lambda: (e := open_event(api, site["id"])) and e["verdict"] == want and e, args.timeout, 2)
            t_correct = time.time() - t0 if correct else None
            final = open_event(api, site["id"])
            got = final["verdict"] if final else "none"
            log(f"  verdict: {got} — {final['summary'] if final else ''}")
            revert()
            t1 = time.time()
            cleared = wait(lambda: not open_event(api, site["id"]), args.timeout, 2)
            t_clear = time.time() - t1 if cleared else None
            ok = got == want and t_clear is not None
            failures += 0 if ok else 1
            rows.append((name, want, got, t_first, t_correct, t_clear, ok, (final or {}).get("evidence", [])))

        write_report(args.out, rows)
        log(f"report written to {args.out}")
    finally:
        procs.stop_all()
        teardown()
        if failures or rows == []:
            for f in ("controller.log", "agent.log"):
                p = os.path.join(work, f)
                if os.path.exists(p):
                    print(f"----- {f} (last 40 lines)")
                    print("".join(open(p).readlines()[-40:]))
        shutil.rmtree(work, ignore_errors=True)
    sys.exit(1 if failures or not rows else 0)


def _ready():
    try:
        with urllib.request.urlopen(f"http://{CTL}:{CTL_PORT}/readyz", timeout=2) as r:
            return r.status == 200
    except Exception:
        return False


def open_event(api, site_id):
    items = api.call("GET", f"/api/v1/events?state=open&site_id={site_id}")["items"]
    return items[0] if items else None


def fmt(t):
    return "—" if t is None else f"{t:.0f} s"


def write_report(path, rows):
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    correct = sum(1 for r in rows if r[6])
    with open(path, "w") as f:
        f.write("# Fault-injection lab results\n\n")
        f.write(f"Run: {time.strftime('%Y-%m-%d %H:%M UTC', time.gmtime())} · evaluation window 40 s, "
                f"tests every 10 s, traceroute every 60 s.\n\n")
        f.write(f"**{correct} of {len(rows)} scenarios attributed correctly.**\n\n")
        f.write("| Scenario | Expected | Verdict | First alert | Correct verdict | Cleared after fix | Pass |\n")
        f.write("| --- | --- | --- | --- | --- | --- | --- |\n")
        for name, want, got, t1, t2, t3, ok, _ in rows:
            f.write(f"| {name} | {want} | {got} | {fmt(t1)} | {fmt(t2)} | {fmt(t3)} | {'yes' if ok else 'NO'} |\n")
        f.write("\n## Evidence reported\n\n")
        for name, *_, ev in rows:
            f.write(f"**{name}**\n\n" + "".join(f"- {e}\n" for e in ev) + "\n")


if __name__ == "__main__":
    main()
