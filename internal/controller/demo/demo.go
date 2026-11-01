// Package demo creates a simulated customer so PathLedger can be shown and
// evaluated without any real branch offices: three clinics, a fortnight of
// measurements with realistic incidents, and a live feed that can inject a
// fault on demand so the real analysis and alerting can be watched working.
//
// All addresses are from ranges reserved for documentation and testing
// (RFC 5737, RFC 2544, RFC 6598) and all names end in .example, so demo data
// can never be mistaken for a real network or provider.
package demo

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// Fault is a simulated problem at one site.
type Fault string

// Faults the simulator can produce. They mirror the analysis verdicts.
const (
	Healthy  Fault = ""
	ISP      Fault = "isp"      // the site's internet link is down
	Local    Fault = "local"    // the site's own router is down
	Upstream Fault = "upstream" // a network beyond the ISP drops traffic
	DNS      Fault = "dns"      // the resolver fails; connections by IP work
	App      Fault = "app"      // one application returns errors
	Offline  Fault = "offline"  // the agent's computer is off: no data at all
)

// ParseFault validates a fault name from the command line.
func ParseFault(s string) (Fault, error) {
	switch f := Fault(strings.ToLower(s)); f {
	case ISP, Local, Upstream, DNS, App, Offline:
		return f, nil
	}
	return "", fmt.Errorf("unknown fault %q (isp, local, upstream, dns, app or offline)", s)
}

// Site is a simulated branch.
type Site struct {
	Name     string
	Location string
	Hostname string
}

// Sites are the demo customer's branches.
var Sites = []Site{
	{"Koramangala clinic", "Bengaluru", "demo-koramangala"},
	{"Indiranagar clinic", "Bengaluru", "demo-indiranagar"},
	{"Whitefield clinic", "Bengaluru", "demo-whitefield"},
}

// Destination addresses (documentation ranges).
const (
	cloudIP   = "203.0.113.10"
	appHost   = "app.demo.example"
	payHost   = "payments.demo.example"
	ispAS     = "Demo ISP"
	transitAS = "Demo Transit"
)

// ASNData maps the demo address ranges to demo networks, in the IPtoASN
// format the controller reads, so traceroute hops are attributed.
const ASNData = "198.18.0.0\t198.18.255.255\t64500\tIN\t" + ispAS + "\n" +
	"198.51.100.0\t198.51.100.255\t64510\tIN\t" + transitAS + "\n" +
	"203.0.113.0\t203.0.113.255\t64520\tIN\tDemo Cloud\n"

// Tests are the demo customer's tests, run at every site.
var Tests = []wire.Test{
	{Kind: wire.KindICMP, Target: cloudIP, IntervalSeconds: 60, TimeoutMillis: 1000, Params: wire.TestParams{Count: 5}},
	{Kind: wire.KindDNS, Target: appHost, IntervalSeconds: 60, TimeoutMillis: 2000},
	{Kind: wire.KindTCP, Target: payHost + ":443", IntervalSeconds: 60, TimeoutMillis: 2000},
	{Kind: wire.KindHTTP, Target: "https://" + appHost + "/health", IntervalSeconds: 60, TimeoutMillis: 5000},
	{Kind: wire.KindTraceroute, Target: cloudIP, IntervalSeconds: 120, TimeoutMillis: 1000, Params: wire.TestParams{MaxHops: 8, Count: 2}},
}

// Incident is a scripted fault in the demo history.
type Incident struct {
	Site     int
	Fault    Fault
	Ago      time.Duration // start, before the seeding time
	Duration time.Duration
}

// History is the scripted fortnight; incidents older than the seeded
// period are skipped.
var History = []Incident{
	{0, ISP, 11*24*time.Hour - 10*time.Hour, 106 * time.Minute},
	{2, Offline, 9*24*time.Hour - 2*time.Hour, 40 * time.Minute},
	{1, Upstream, 6*24*time.Hour - 23*time.Hour, 75 * time.Minute},
	{2, ISP, 5*24*time.Hour - 16*time.Hour, 52 * time.Minute},
	{0, ISP, 4*24*time.Hour - 19*time.Hour, 25 * time.Minute},
	{1, DNS, 3*24*time.Hour - 11*time.Hour, 18 * time.Minute},
	{0, Local, 2*24*time.Hour - 9*time.Hour, 14 * time.Minute},
	{0, App, 1*24*time.Hour - 14*time.Hour, 70 * time.Minute},
}

// Summary and evidence as the analysis engine would write them.
func Summary(f Fault) (string, []string) {
	switch f {
	case ISP:
		return "The site's router answers but nothing beyond it does; the internet connection (ISP) is down or degraded.",
			[]string{"3 different destinations are affected and none are healthy", "the path stops after hop 1 (local gateway)"}
	case Local:
		return "The site's own router is not responding; the problem is inside the site's network.",
			[]string{"traceroute gets no reply from the first hop (the local gateway)"}
	case Upstream:
		return "Traffic leaves the ISP normally and is lost in AS64510 " + transitAS + "; the fault is upstream of the ISP.",
			[]string{"3 different destinations are affected and none are healthy", "the path stops after hop 4 (AS64510 " + transitAS + ")"}
	case DNS:
		return "Name resolution is failing while connections to other destinations work; check the DNS resolver in use.",
			[]string{"dns " + appHost + " is down (dns)"}
	case App:
		return "Only specific destinations are affected and the site's connection otherwise works; the problem is with those services or the route to them.",
			[]string{"http https://" + appHost + "/health is down (http_status)", "other destinations from this site are healthy"}
	case Offline:
		return "No data from this site's agents; the site may be offline or the agent stopped.", nil
	}
	return "", nil
}

// Result simulates one measurement of t at site s. ok is false when the
// fault means nothing would be reported (the agent's computer is off).
func Result(t wire.Test, s int, at time.Time, f Fault, rng *rand.Rand) (r wire.Result, ok bool) {
	if f == Offline {
		return r, false
	}
	r = wire.Result{TestID: t.ID, Time: at, Kind: t.Kind, Target: t.Target, Details: map[string]string{}}
	down := f == ISP || f == Local || f == Upstream
	// Each site has its own baseline; evenings are a little slower.
	busy := 1.0
	if h := at.Hour(); h >= 18 && h <= 22 {
		busy = 1.25
	}
	lat := func(base, spread float64) *float64 {
		v := (base + float64(s)*3 + rng.Float64()*spread) * busy
		if rng.Float64() < 0.02 {
			v *= 3 // occasional spike
		}
		return wire.Float(math.Round(v*100) / 100)
	}
	fail := func(c wire.ErrorClass, msg string) wire.Result {
		r.Success, r.ErrorClass, r.Error = false, c, msg
		return r
	}

	switch t.Kind {
	case wire.KindICMP:
		if down {
			r.LossPct = wire.Float(100)
			r.Details["sent"], r.Details["received"] = "5", "0"
			return fail(wire.ErrClassLoss, "no replies to 5 echo requests"), true
		}
		lost := 0
		if rng.Float64() < 0.04 {
			lost = 1
		}
		r.Success, r.LatencyMS = true, lat(11, 5)
		r.LossPct = wire.Float(float64(lost) * 20)
		r.JitterMS = wire.Float(math.Round(rng.Float64()*300) / 100)
		r.Details["sent"], r.Details["received"] = "5", fmt.Sprint(5-lost)
		return r, true
	case wire.KindDNS:
		if down {
			return fail(wire.ErrClassTimeout, "lookup "+appHost+": i/o timeout"), true
		}
		if f == DNS {
			return fail(wire.ErrClassDNS, "lookup "+appHost+": server misbehaving"), true
		}
		r.Success, r.LatencyMS = true, lat(18, 22)
		r.Details["answers"] = "203.0.113.20"
	case wire.KindTCP:
		if down {
			return fail(wire.ErrClassTimeout, "dial tcp: i/o timeout"), true
		}
		if f == DNS {
			return fail(wire.ErrClassDNS, "lookup "+payHost+": server misbehaving"), true
		}
		r.Success, r.LatencyMS = true, lat(28, 30)
	case wire.KindHTTP:
		switch {
		case down:
			return fail(wire.ErrClassTimeout, "context deadline exceeded"), true
		case f == DNS:
			return fail(wire.ErrClassDNS, "lookup "+appHost+": server misbehaving"), true
		case f == App:
			r.Details["status"] = "503"
			r.LatencyMS = lat(40, 20)
			return fail(wire.ErrClassHTTPStatus, "unexpected status 503"), true
		}
		r.Success, r.LatencyMS = true, lat(180, 120)
		r.Details["status"] = "200"
	case wire.KindTraceroute:
		return trace(r, s, f, rng), true
	}
	return r, true
}

// path is the simulated route from site s to the cloud destination.
func path(s int) []string {
	return []string{
		fmt.Sprintf("192.168.%d.1", 10+s), // the clinic's router
		fmt.Sprintf("100.64.%d.1", s),     // ISP carrier-grade NAT
		fmt.Sprintf("198.18.%d.1", s),     // ISP core (AS64500)
		fmt.Sprintf("198.51.100.%d", 1+s), // transit (AS64510)
		cloudIP,                           // destination (AS64520)
	}
}

func trace(r wire.Result, s int, f Fault, rng *rand.Rand) wire.Result {
	hops := path(s)
	answering := len(hops)
	switch f {
	case Local:
		answering = 0
	case ISP:
		answering = 1
	case Upstream:
		answering = 4
	}
	rtt := 1.0
	for i := 0; i < answering; i++ {
		rtt += 2 + float64(i)*2.5 + rng.Float64()*2
		avg := math.Round(rtt*1000) / 1000
		r.Hops = append(r.Hops, wire.Hop{TTL: i + 1, Addr: hops[i], Sent: 2, Received: 2,
			AvgMS: wire.Float(avg), MinMS: wire.Float(avg - 0.3), MaxMS: wire.Float(avg + 0.4)})
	}
	if answering == len(hops) {
		last := r.Hops[len(r.Hops)-1]
		r.Success, r.LatencyMS, r.LossPct = true, last.AvgMS, wire.Float(0)
		r.Details["hops"] = fmt.Sprint(len(r.Hops))
		return r
	}
	// A real traceroute gives up after 5 silent hops.
	for i := answering; i < answering+5; i++ {
		r.Hops = append(r.Hops, wire.Hop{TTL: i + 1, Sent: 2})
	}
	r.Details["hops"] = fmt.Sprint(len(r.Hops))
	r.Success, r.ErrorClass, r.Error = false, wire.ErrClassUnreach, "destination did not answer"
	return r
}
