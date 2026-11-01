package analytics

import (
	"testing"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

func th(id string, kind wire.ProbeKind, target string, st Status) TestHealth {
	return TestHealth{TestID: id, Kind: kind, Target: target, Status: st}
}

func hop(ttl int, addr, scope string, asn int, received int) wire.Hop {
	return wire.Hop{TTL: ttl, Addr: addr, Scope: scope, ASN: asn, Sent: 3, Received: received}
}

func TestAttribute(t *testing.T) {
	const isp = 24309
	cases := []struct {
		name string
		snap SiteSnapshot
		want Verdict
	}{
		{
			name: "all healthy",
			snap: SiteSnapshot{Tests: []TestHealth{th("a", wire.KindTCP, "x.com:443", StatusOK)}},
			want: "",
		},
		{
			name: "gateway silent",
			snap: SiteSnapshot{
				Tests: []TestHealth{th("a", wire.KindTCP, "x.com:443", StatusDown), th("b", wire.KindICMP, "1.1.1.1", StatusDown)},
				Path:  &Path{Hops: []wire.Hop{{TTL: 1, Sent: 3}, {TTL: 2, Sent: 3}}},
			},
			want: VerdictLocal,
		},
		{
			name: "gateway answers, nothing beyond",
			snap: SiteSnapshot{
				Tests: []TestHealth{th("a", wire.KindTCP, "x.com:443", StatusDown), th("b", wire.KindICMP, "1.1.1.1", StatusDown)},
				Path:  &Path{Hops: []wire.Hop{hop(1, "192.168.1.1", "lan", 0, 3), {TTL: 2, Sent: 3}}},
			},
			want: VerdictISP,
		},
		{
			name: "everything down, no traceroute",
			snap: SiteSnapshot{Tests: []TestHealth{
				th("a", wire.KindHTTP, "https://billing.example.com/health", StatusDown),
				th("b", wire.KindICMP, "8.8.8.8", StatusDegraded),
			}},
			want: VerdictISP,
		},
		{
			name: "lost beyond the ISP",
			snap: SiteSnapshot{
				ISPASN: isp,
				Tests:  []TestHealth{th("a", wire.KindTCP, "x.com:443", StatusDown), th("b", wire.KindICMP, "1.1.1.1", StatusDown)},
				Path: &Path{Hops: []wire.Hop{
					hop(1, "192.168.1.1", "lan", 0, 3), hop(2, "49.205.0.1", "public", isp, 3),
					hop(3, "4.68.0.1", "public", 3356, 3), {TTL: 4, Sent: 3},
				}},
			},
			want: VerdictUpstream,
		},
		{
			name: "lost inside the ISP",
			snap: SiteSnapshot{
				ISPASN: isp,
				Tests:  []TestHealth{th("a", wire.KindTCP, "x.com:443", StatusDown), th("b", wire.KindICMP, "1.1.1.1", StatusDown)},
				Path:   &Path{Hops: []wire.Hop{hop(1, "192.168.1.1", "lan", 0, 3), hop(2, "49.205.0.1", "public", isp, 3), {TTL: 3, Sent: 3}}},
			},
			want: VerdictISP,
		},
		{
			name: "only DNS failing",
			snap: SiteSnapshot{Tests: []TestHealth{
				{TestID: "d", Kind: wire.KindDNS, Target: "example.com", Status: StatusDown, ErrorClass: wire.ErrClassTimeout},
				th("a", wire.KindICMP, "1.1.1.1", StatusOK),
			}},
			want: VerdictDNS,
		},
		{
			name: "one service down here and elsewhere",
			snap: SiteSnapshot{Tests: []TestHealth{
				{TestID: "a", Kind: wire.KindHTTP, Target: "https://erp.example.com/", Status: StatusDown, OtherSitesAffected: 4, OtherSitesTotal: 5},
				th("b", wire.KindICMP, "1.1.1.1", StatusOK),
			}},
			want: VerdictApp,
		},
		{
			name: "same host via two probes counts as one destination",
			snap: SiteSnapshot{Tests: []TestHealth{
				th("a", wire.KindHTTP, "https://erp.example.com/login", StatusDown),
				th("b", wire.KindTCP, "erp.example.com:443", StatusDown),
			}},
			want: VerdictUnknown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, ok := Attribute(c.snap)
			if c.want == "" {
				if ok {
					t.Fatalf("expected no verdict, got %+v", a)
				}
				return
			}
			if !ok || a.Verdict != c.want {
				t.Fatalf("got %q (%s), want %q; evidence %v", a.Verdict, a.Summary, c.want, a.Evidence)
			}
			if a.Summary == "" || len(a.Evidence) == 0 || len(a.Affected) == 0 {
				t.Fatalf("verdict missing explanation: %+v", a)
			}
		})
	}
}

func TestDestinationOf(t *testing.T) {
	cases := map[string]string{
		"https://ERP.example.com:8443/x": "erp.example.com",
		"erp.example.com:443":            "erp.example.com",
		"[2606:4700::1111]:443":          "2606:4700::1111",
		"1.1.1.1":                        "1.1.1.1",
	}
	for in, want := range cases {
		kind := wire.KindTCP
		if in[0] == 'h' {
			kind = wire.KindHTTP
		}
		if got := destinationOf(kind, in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}
