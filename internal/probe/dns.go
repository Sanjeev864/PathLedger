package probe

import (
	"context"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// DNS measures how long it takes to resolve a name, either through the
// system resolver or through a specific server (params.resolver), which lets
// customers compare their ISP's resolver against public ones.
type DNS struct {
	// Dial overrides how the resolver reaches a specific server; used in tests.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// Probe implements Prober.
func (p *DNS) Probe(ctx context.Context, t wire.Test) wire.Result {
	ctx, cancel := context.WithTimeout(ctx, t.Timeout())
	defer cancel()

	res := net.DefaultResolver
	details := map[string]string{"resolver": "system"}
	if server := t.Params.Resolver; server != "" {
		details["resolver"] = server
		dial := p.Dial
		if dial == nil {
			var d net.Dialer
			dial = d.DialContext
		}
		res = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dial(ctx, network, server)
			},
		}
	}

	start := time.Now()
	addrs, err := res.LookupNetIP(ctx, "ip", t.Target)
	elapsed := time.Since(start)
	if err != nil {
		r := failure(classify(err), err)
		details["lookup_ms"] = msString(elapsed)
		r.Details = details
		return r
	}
	answers := make([]string, 0, len(addrs))
	for _, a := range addrs {
		answers = append(answers, a.Unmap().String())
	}
	sort.Strings(answers)
	details["answers"] = truncate(strings.Join(answers, ","), wire.MaxDetailValueLen)
	return wire.Result{Success: true, LatencyMS: wire.Float(ms(elapsed)), Details: details}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
