package probe

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// fakeSys simulates a path: hops[i] answers TTL i+1; an invalid address is
// silence. The destination answers at TTL >= len(hops)+1.
type fakeSys struct {
	dst   netip.Addr
	hops  []netip.Addr
	lossy map[int]bool // echo replies lost on these calls
	calls int
}

func (f *fakeSys) ping(dst netip.Addr, ttl int, _ time.Duration, _ []byte) (sysReply, error) {
	f.calls++
	if f.lossy[f.calls] {
		return sysReply{}, errSysTimeout
	}
	if ttl <= len(f.hops) {
		h := f.hops[ttl-1]
		if !h.IsValid() {
			return sysReply{}, errSysTimeout
		}
		return sysReply{kind: replyTTLExpired, from: h, rtt: time.Duration(ttl) * time.Millisecond}, nil
	}
	return sysReply{kind: replyEcho, from: dst, rtt: 20 * time.Millisecond}, nil
}

func TestSysEcho(t *testing.T) {
	dst := netip.MustParseAddr("192.0.2.9")
	f := &fakeSys{dst: dst, lossy: map[int]bool{2: true}}
	r := sysEcho(context.Background(), f, dst, 4, time.Millisecond, time.Second, map[string]string{})
	if !r.Success || *r.LossPct != 25 || *r.LatencyMS != 20 || r.Details["socket"] != "system" {
		t.Fatalf("%+v", r)
	}
	all := &fakeSys{dst: dst, lossy: map[int]bool{1: true, 2: true}}
	if r := sysEcho(context.Background(), all, dst, 2, time.Millisecond, time.Second, map[string]string{}); r.Success || r.ErrorClass != wire.ErrClassLoss {
		t.Fatalf("total loss: %+v", r)
	}
}

func TestSysTraceroute(t *testing.T) {
	dst := netip.MustParseAddr("192.0.2.9")
	f := &fakeSys{dst: dst, hops: []netip.Addr{netip.MustParseAddr("192.168.1.1"), {}, netip.MustParseAddr("100.64.0.1")}}
	tr := &tracer{dst: dst, hop: sysHop(f, dst)}
	hops, reached := tr.run(context.Background(), 10, 2, time.Second)
	if !reached || len(hops) != 4 {
		t.Fatalf("reached=%v hops=%+v", reached, hops)
	}
	if hops[0].Addr != "192.168.1.1" || hops[1].Received != 0 || hops[2].Addr != "100.64.0.1" || hops[3].Addr != dst.String() {
		t.Fatalf("hops %+v", hops)
	}
}
