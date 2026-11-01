package probe

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// buildICMPError crafts an ICMP error quoting a UDP probe.
func buildICMPError(typ, code byte, dst netip.Addr, srcPort, dstPort, udpLen uint16) []byte {
	b := make([]byte, 8+20+8)
	b[0], b[1] = typ, code
	ip := b[8:]
	ip[0] = 0x45 // IPv4, IHL 5
	ip[9] = 17   // UDP
	d := dst.As4()
	copy(ip[16:20], d[:])
	udp := ip[20:]
	binary.BigEndian.PutUint16(udp[0:], srcPort)
	binary.BigEndian.PutUint16(udp[2:], dstPort)
	binary.BigEndian.PutUint16(udp[4:], udpLen)
	return b
}

func TestParseTraceICMP(t *testing.T) {
	dst := netip.MustParseAddr("203.0.113.9")
	q, ok := parseTraceICMP(buildICMPError(icmpTimeExceeded, 0, dst, 40000, traceDstPort, 25))
	if !ok || q.dst != dst || q.srcPort != 40000 || q.dstPort != traceDstPort || q.udpLen != 25 {
		t.Fatalf("parse failed: %+v %v", q, ok)
	}
	if _, ok := parseTraceICMP([]byte{0, 0, 0, 0}); ok {
		t.Fatal("short packet accepted")
	}
	echo := buildICMPError(icmpTimeExceeded, 0, dst, 1, 2, 3)
	echo[0] = icmpEchoReply
	if _, ok := parseTraceICMP(echo); ok {
		t.Fatal("echo reply accepted as trace reply")
	}
	if unreachableReason(13) != "admin" || unreachableReason(1) != "host" || unreachableReason(0) != "net" {
		t.Fatal("unreachable codes misclassified")
	}
}

// simNet simulates routers: path[ttl-1] answers with time-exceeded ("" is a
// silent hop); a TTL past the path reaches the destination.
type simNet struct {
	mu      sync.Mutex
	path    []string
	dst     netip.Addr
	srcPort uint16
	finale  func() (typ, code byte) // reply from the last hop
	queue   chan simPacket
	ttl     int
}

type simPacket struct {
	b    []byte
	from net.Addr
}

type simUDP struct{ n *simNet }

func (u simUDP) SetTTL(ttl int) error { u.n.mu.Lock(); u.n.ttl = ttl; u.n.mu.Unlock(); return nil }
func (u simUDP) WriteTo(p []byte, _ net.Addr) (int, error) {
	n := u.n
	n.mu.Lock()
	ttl := n.ttl
	n.mu.Unlock()
	udpLen := uint16(8 + len(p))
	var from string
	typ, code := byte(icmpTimeExceeded), byte(0)
	if ttl <= len(n.path) {
		from = n.path[ttl-1]
		if from == "" {
			return len(p), nil // silent router
		}
	} else {
		from = n.dst.String()
		typ, code = n.finale()
	}
	// A stray packet for someone else's probe arrives first and must be ignored.
	n.queue <- simPacket{buildICMPError(icmpTimeExceeded, 0, n.dst, n.srcPort+1, traceDstPort, udpLen), &net.IPAddr{IP: net.ParseIP(from)}}
	n.queue <- simPacket{buildICMPError(typ, code, n.dst, n.srcPort, traceDstPort, udpLen), &net.IPAddr{IP: net.ParseIP(from)}}
	return len(p), nil
}
func (u simUDP) ReadFrom([]byte) (int, net.Addr, error) { return 0, nil, nil }
func (u simUDP) Close() error                           { return nil }
func (u simUDP) LocalAddr() net.Addr                    { return &net.UDPAddr{Port: int(u.n.srcPort)} }
func (u simUDP) SetDeadline(time.Time) error            { return nil }
func (u simUDP) SetReadDeadline(time.Time) error        { return nil }
func (u simUDP) SetWriteDeadline(time.Time) error       { return nil }

type simICMP struct {
	n        *simNet
	deadline time.Time
}

func (c *simICMP) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case p := <-c.n.queue:
		return copy(b, p.b), p.from, nil
	case <-time.After(time.Until(c.deadline)):
		return 0, nil, os.ErrDeadlineExceeded
	}
}
func (c *simICMP) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }
func (c *simICMP) Close() error                              { return nil }
func (c *simICMP) LocalAddr() net.Addr                       { return &net.IPAddr{} }
func (c *simICMP) SetDeadline(t time.Time) error             { c.deadline = t; return nil }
func (c *simICMP) SetReadDeadline(t time.Time) error         { c.deadline = t; return nil }
func (c *simICMP) SetWriteDeadline(time.Time) error          { return nil }

func runSim(path []string, finale func() (byte, byte), maxHops int) ([]wire.Hop, bool) {
	n := &simNet{path: path, dst: netip.MustParseAddr("203.0.113.9"), srcPort: 40000, finale: finale, queue: make(chan simPacket, 16)}
	tr := &tracer{icmp: &simICMP{n: n}, udp: simUDP{n}, dst: n.dst, srcPort: n.srcPort, buf: make([]byte, 1500)}
	return tr.run(context.Background(), maxHops, 2, 20*time.Millisecond)
}

func portUnreachable() (byte, byte) { return icmpDestUnreachable, 3 }

func TestTracerReachesDestination(t *testing.T) {
	hops, reached := runSim([]string{"192.168.1.1", "", "100.64.0.1"}, portUnreachable, 30)
	if !reached || len(hops) != 4 {
		t.Fatalf("reached=%v hops=%d", reached, len(hops))
	}
	if hops[0].Addr != "192.168.1.1" || hops[0].Received != 2 || hops[0].AvgMS == nil {
		t.Fatalf("hop 1 wrong: %+v", hops[0])
	}
	if hops[1].Addr != "" || hops[1].Received != 0 || hops[1].Sent != 2 {
		t.Fatalf("silent hop wrong: %+v", hops[1])
	}
	if hops[3].Addr != "203.0.113.9" {
		t.Fatalf("destination hop wrong: %+v", hops[3])
	}
	r := traceResult(hops, reached, map[string]string{})
	if !r.Success || *r.LossPct != 0 || r.Details["hops"] != "4" {
		t.Fatalf("result wrong: %+v", r)
	}
}

func TestTracerReportsFilteredPath(t *testing.T) {
	hops, reached := runSim([]string{"192.168.1.1"}, func() (byte, byte) { return icmpDestUnreachable, 13 }, 30)
	if reached || hops[len(hops)-1].Unreachable != "admin" {
		t.Fatalf("expected admin-prohibited stop, got reached=%v %+v", reached, hops)
	}
	r := traceResult(hops, reached, map[string]string{})
	if r.Success || r.ErrorClass != wire.ErrClassUnreach {
		t.Fatalf("result wrong: %+v", r)
	}
}

func TestTracerStopsAfterSilence(t *testing.T) {
	path := make([]string, 20)
	path[0] = "192.168.1.1"
	hops, reached := runSim(path, portUnreachable, 30)
	if reached || len(hops) != 1+traceSilentStop {
		t.Fatalf("expected stop after %d silent hops, got %d hops", traceSilentStop, len(hops))
	}
}

func TestTracerouteLoopback(t *testing.T) {
	conn, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		t.Skipf("raw ICMP unavailable: %v", err)
	}
	conn.Close()
	tc := wire.Test{ID: "t", Kind: wire.KindTraceroute, Target: "127.0.0.1", IntervalSeconds: 300, TimeoutMillis: 500}
	r := (&Traceroute{Resolver: net.DefaultResolver}).Probe(context.Background(), tc)
	if !r.Success || len(r.Hops) != 1 || r.Hops[0].Addr != "127.0.0.1" {
		t.Fatalf("expected one-hop path to loopback, got %+v", r)
	}
}
