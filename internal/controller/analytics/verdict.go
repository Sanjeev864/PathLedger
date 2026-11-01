// Package analytics turns raw probe results into events with a fault
// verdict: is the problem in the customer's own network, their ISP, a
// network further upstream, or the application itself?
//
// The attribution rules are deliberately simple and explainable. Every
// verdict carries the evidence that produced it, because a customer forwarding
// it to their ISP needs to be able to defend it.
package analytics

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// Status is the health of one test at one site over the evaluation window.
type Status string

// Test statuses.
const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded" // reachable but slow or lossy
	StatusDown     Status = "down"     // mostly failing
	StatusUnknown  Status = "unknown"  // not enough data
)

// Verdict names where a fault most likely sits.
type Verdict string

// Verdicts, ordered from closest to the customer to furthest away.
const (
	VerdictLocal    Verdict = "local"    // the site's own LAN, Wi-Fi or router
	VerdictISP      Verdict = "isp"      // the site's internet provider
	VerdictUpstream Verdict = "upstream" // a network beyond the ISP
	VerdictDNS      Verdict = "dns"      // name resolution only
	VerdictApp      Verdict = "app"      // one destination or service
	VerdictUnknown  Verdict = "unknown"
)

// TestHealth is one test's state at a site.
type TestHealth struct {
	TestID     string
	Kind       wire.ProbeKind
	Target     string
	Status     Status
	ErrorClass wire.ErrorClass
	// OtherSitesAffected and OtherSitesTotal describe the same test at the
	// tenant's other sites (tenant-wide tests only).
	OtherSitesAffected int
	OtherSitesTotal    int
}

// Path is the site's most recent traceroute.
type Path struct {
	Target  string
	Reached bool
	Hops    []wire.Hop
}

// SiteSnapshot is everything the attribution rules look at.
type SiteSnapshot struct {
	Tests []TestHealth
	Path  *Path
	// ISPASN is the site's provider, learned from healthy traceroutes
	// (the first public hop's AS). Zero if unknown.
	ISPASN int
}

// Attribution is a verdict plus the reasoning behind it.
type Attribution struct {
	Verdict  Verdict  `json:"verdict"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence"`
	Affected []string `json:"affected_tests"`
}

func affected(s Status) bool { return s == StatusDown || s == StatusDegraded }

// Attribute decides where a site's problem lies. It returns ok=false when
// nothing at the site is unhealthy.
func Attribute(snap SiteSnapshot) (Attribution, bool) {
	var bad, good []TestHealth
	for _, t := range snap.Tests {
		switch {
		case affected(t.Status):
			bad = append(bad, t)
		case t.Status == StatusOK:
			good = append(good, t)
		}
	}
	if len(bad) == 0 {
		return Attribution{}, false
	}
	a := Attribution{}
	for _, t := range bad {
		a.Affected = append(a.Affected, t.TestID)
		a.Evidence = append(a.Evidence, fmt.Sprintf("%s %s is %s%s", t.Kind, t.Target, t.Status, errSuffix(t.ErrorClass)))
	}
	sort.Strings(a.Affected)
	badDests, goodDests := destinations(bad), destinations(good)
	hop := pathBreak(snap.Path)

	// 1. The LAN gateway itself is not answering: the fault is on site.
	if hop.lanSilent {
		a.Verdict = VerdictLocal
		a.Summary = "The site's own router is not responding; the problem is inside the site's network."
		a.Evidence = append(a.Evidence, "traceroute gets no reply from the first hop (the local gateway)")
		return a, true
	}

	// 2. Only name resolution fails, everything reached by IP is fine.
	if allDNS(bad) && len(good) > 0 {
		a.Verdict = VerdictDNS
		a.Summary = "Name resolution is failing while connections to other destinations work; check the DNS resolver in use."
		return a, true
	}

	siteWide := len(badDests) >= 2 && len(goodDests) == 0
	if siteWide {
		a.Evidence = append(a.Evidence, fmt.Sprintf("%d different destinations are affected and none are healthy", len(badDests)))
		if hop.lastAnswer != nil {
			a.Evidence = append(a.Evidence, fmt.Sprintf("the path stops after hop %d (%s)", hop.lastAnswer.TTL, describeHop(*hop.lastAnswer)))
		}
		switch {
		case hop.lastAnswer != nil && hop.lastAnswer.Scope == "lan" && hop.afterLANSilent:
			// The gateway answers but nothing beyond it does: the ISP link is down.
			a.Verdict = VerdictISP
			a.Summary = "The site's router answers but nothing beyond it does; the internet connection (ISP) is down or degraded."
		case hop.lastAnswer != nil && snap.ISPASN != 0 && hop.lastAnswer.ASN != 0 && hop.lastAnswer.ASN != snap.ISPASN && hop.lastAnswer.Scope == "public":
			a.Verdict = VerdictUpstream
			a.Summary = fmt.Sprintf("Traffic leaves the ISP normally and is lost in %s; the fault is upstream of the ISP.", asLabel(*hop.lastAnswer))
		default:
			a.Verdict = VerdictISP
			a.Summary = "Every destination tested from this site is affected at once; the most likely cause is the site's internet connection (ISP)."
		}
		return a, true
	}

	// 3. Some destinations are fine, so the site's connection works. The
	// affected destinations are the problem — check if other sites agree.
	if len(goodDests) > 0 {
		cross := false
		for _, t := range bad {
			if t.OtherSitesTotal > 0 && 2*t.OtherSitesAffected >= t.OtherSitesTotal {
				cross = true
				a.Evidence = append(a.Evidence, fmt.Sprintf("%s also affected at %d of %d other sites", t.Target, t.OtherSitesAffected, t.OtherSitesTotal))
			}
		}
		a.Evidence = append(a.Evidence, fmt.Sprintf("%d other destination(s) from this site are healthy", len(goodDests)))
		a.Verdict = VerdictApp
		if cross {
			a.Summary = "The affected service is failing from several sites while other destinations work; the problem is with the service itself."
		} else {
			a.Summary = "Only specific destinations are affected and the site's connection otherwise works; the problem is with those services or the route to them."
		}
		return a, true
	}

	a.Verdict = VerdictUnknown
	a.Summary = "Not enough healthy reference tests to locate the fault; add tests to at least two different destinations."
	return a, true
}

type breakInfo struct {
	lanSilent      bool      // hop 1 never answered
	afterLANSilent bool      // only LAN hops answered
	lastAnswer     *wire.Hop // furthest hop that answered
}

// pathBreak finds where a failed traceroute stopped. A path that reached
// its destination tells us nothing about a break.
func pathBreak(p *Path) breakInfo {
	var b breakInfo
	if p == nil || p.Reached || len(p.Hops) == 0 {
		return b
	}
	if p.Hops[0].Received == 0 {
		b.lanSilent = true
		return b
	}
	onlyLAN := true
	for i := range p.Hops {
		h := p.Hops[i]
		if h.Received > 0 {
			b.lastAnswer = &p.Hops[i]
			if h.Scope != "lan" {
				onlyLAN = false
			}
		}
	}
	b.afterLANSilent = onlyLAN
	return b
}

func destinations(ts []TestHealth) map[string]bool {
	out := map[string]bool{}
	for _, t := range ts {
		out[destinationOf(t.Kind, t.Target)] = true
	}
	return out
}

// destinationOf normalises a target to its host so that, for example, a TCP
// and an HTTP test against the same server count as one destination.
func destinationOf(kind wire.ProbeKind, target string) string {
	host := target
	if kind == wire.KindHTTP {
		host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

func allDNS(ts []TestHealth) bool {
	for _, t := range ts {
		if t.Kind != wire.KindDNS && t.ErrorClass != wire.ErrClassDNS {
			return false
		}
	}
	return true
}

func errSuffix(c wire.ErrorClass) string {
	if c == "" {
		return ""
	}
	return " (" + string(c) + ")"
}

func describeHop(h wire.Hop) string {
	label := h.Addr
	switch {
	case h.Scope == "lan":
		label += ", local network"
	case h.Scope == "cgnat":
		label += ", ISP carrier-grade NAT"
	case h.ASN != 0:
		label += ", " + asLabel(h)
	}
	return label
}

func asLabel(h wire.Hop) string {
	if h.ASName != "" {
		return fmt.Sprintf("AS%d %s", h.ASN, h.ASName)
	}
	return fmt.Sprintf("AS%d", h.ASN)
}
