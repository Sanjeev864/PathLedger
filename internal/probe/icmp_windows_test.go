//go:build windows

package probe

import (
	"context"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// These run on Windows CI and check the iphlpapi bindings for real.

func TestWindowsICMPLoopback(t *testing.T) {
	r := (&ICMP{}).Probe(context.Background(), wire.Test{Kind: wire.KindICMP, Target: "127.0.0.1", TimeoutMillis: 1000, Params: wire.TestParams{Count: 3}})
	if !r.Success || r.Details["socket"] != "system" || *r.LossPct != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestWindowsTracerouteLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r := (&Traceroute{}).Probe(ctx, wire.Test{Kind: wire.KindTraceroute, Target: "127.0.0.1", TimeoutMillis: 1000,
		Params: wire.TestParams{MaxHops: 3, Count: 1}})
	if !r.Success || len(r.Hops) != 1 || r.Hops[0].Addr != "127.0.0.1" {
		t.Fatalf("%+v hops=%+v", r, r.Hops)
	}
}
