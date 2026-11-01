# Fault-injection lab results

Run: 2026-11-01 14:50 UTC · evaluation window 40 s, tests every 10 s, traceroute every 60 s.

**4 of 4 scenarios attributed correctly.**

| Scenario | Expected | Verdict | First alert | Correct verdict | Cleared after fix | Pass |
| --- | --- | --- | --- | --- | --- | --- |
| LAN router unreachable | local | local | 20 s | 64 s | 40 s | yes |
| ISP link down | isp | isp | 20 s | 30 s | 40 s | yes |
| Transit network fails | upstream | upstream | 24 s | 70 s | 38 s | yes |
| One application down | app | app | 30 s | 30 s | 40 s | yes |

## Evidence reported

**LAN router unreachable**

- tcp 203.0.113.10:8080 is down (timeout)
- http http://203.0.113.20:8080/ is down (unreachable)
- icmp 203.0.113.10 is down (loss)
- traceroute gets no reply from the first hop (the local gateway)

**ISP link down**

- tcp 203.0.113.10:8080 is down (unreachable)
- http http://203.0.113.20:8080/ is down (unreachable)
- icmp 203.0.113.10 is down (loss)
- 2 different destinations are affected and none are healthy
- the path stops after hop 1 (10.0.1.1, local network)

**Transit network fails**

- tcp 203.0.113.10:8080 is down (unreachable)
- http http://203.0.113.20:8080/ is down (unreachable)
- icmp 203.0.113.10 is down (loss)
- 2 different destinations are affected and none are healthy
- the path stops after hop 3 (198.51.100.2, AS64510 Lab Transit)

**One application down**

- http http://203.0.113.20:8080/ is down (refused)
- 1 other destination(s) from this site are healthy

