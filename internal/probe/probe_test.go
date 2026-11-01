package probe

import (
	"context"
	"crypto/x509"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

func test(kind wire.ProbeKind, target string) wire.Test {
	return wire.Test{ID: "t1", Kind: kind, Target: target, IntervalSeconds: 60, TimeoutMillis: 2000}
}

func TestTCPSuccessAndRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	p := &TCP{Resolver: net.DefaultResolver}
	r := p.Probe(context.Background(), test(wire.KindTCP, ln.Addr().String()))
	if !r.Success || r.LatencyMS == nil || r.Details["remote_ip"] != "127.0.0.1" {
		t.Fatalf("expected success, got %+v", r)
	}
	addr := ln.Addr().String()
	ln.Close()
	r = p.Probe(context.Background(), test(wire.KindTCP, addr))
	if r.Success || r.ErrorClass != wire.ErrClassRefused {
		t.Fatalf("expected refused, got %+v", r)
	}
}

func TestTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	target := strings.TrimPrefix(srv.URL, "https://")

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	// httptest certificates are issued for "example.com" and 127.0.0.1.
	ok := (&TLS{Resolver: net.DefaultResolver, RootCAs: pool}).Probe(context.Background(), test(wire.KindTLS, target))
	if !ok.Success || ok.Details["tls_version"] == "" || ok.Details["cert_days_left"] == "" {
		t.Fatalf("expected handshake success, got %+v", ok)
	}

	bad := (&TLS{Resolver: net.DefaultResolver}).Probe(context.Background(), test(wire.KindTLS, target))
	if bad.Success || bad.ErrorClass != wire.ErrClassTLS {
		t.Fatalf("expected untrusted certificate failure, got %+v", bad)
	}
}

func TestHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("hello"))
		case "/created":
			w.WriteHeader(http.StatusCreated)
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	p := &HTTP{}

	r := p.Probe(context.Background(), test(wire.KindHTTP, srv.URL+"/ok"))
	if !r.Success || r.Details["status"] != "200" || r.Details["bytes"] != "5" || r.Details["ttfb_ms"] == "" {
		t.Fatalf("expected 200 success, got %+v", r)
	}
	r = p.Probe(context.Background(), test(wire.KindHTTP, srv.URL+"/redirect"))
	if !r.Success || r.Details["status"] != "302" {
		t.Fatalf("redirect should be measured, not followed: %+v", r)
	}
	r = p.Probe(context.Background(), test(wire.KindHTTP, srv.URL+"/fail"))
	if r.Success || r.ErrorClass != wire.ErrClassHTTPStatus || r.LatencyMS == nil {
		t.Fatalf("expected status failure with latency, got %+v", r)
	}
	tc := test(wire.KindHTTP, srv.URL+"/ok")
	tc.Params.ExpectStatus = http.StatusCreated
	if r = p.Probe(context.Background(), tc); r.Success {
		t.Fatalf("200 must fail when 201 is expected: %+v", r)
	}
}

func TestHTTPTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	tc := test(wire.KindHTTP, srv.URL)
	tc.TimeoutMillis = 200
	r := (&HTTP{}).Probe(context.Background(), tc)
	if r.Success || r.ErrorClass != wire.ErrClassTimeout {
		t.Fatalf("expected timeout, got %+v", r)
	}
}

// fakeDNS answers every A query with 192.0.2.10 and returns NXDOMAIN for
// names starting with "missing".
func fakeDNS(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			if len(q) < 12 {
				continue
			}
			// Question name ends at the zero-length label; then QTYPE, QCLASS.
			end := 12
			for end < len(q) && q[end] != 0 {
				end += int(q[end]) + 1
			}
			qEnd := end + 5
			if qEnd > len(q) {
				continue
			}
			qtype := binary.BigEndian.Uint16(q[end+1:])
			name := string(q[13 : 13+int(q[12])])
			resp := append([]byte{}, q[:qEnd]...)
			resp[2], resp[3] = 0x81, 0x80 // response, recursion available
			binary.BigEndian.PutUint16(resp[6:], 0)
			switch {
			case strings.HasPrefix(name, "missing"):
				resp[3] |= 3 // NXDOMAIN
			case qtype == 1:
				binary.BigEndian.PutUint16(resp[6:], 1)
				resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 10)
			}
			binary.BigEndian.PutUint16(resp[8:], 0)
			binary.BigEndian.PutUint16(resp[10:], 0)
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	return pc.LocalAddr().String()
}

func TestDNS(t *testing.T) {
	server := fakeDNS(t)
	tc := test(wire.KindDNS, "branch.example")
	tc.Params.Resolver = server
	r := (&DNS{}).Probe(context.Background(), tc)
	if !r.Success || r.Details["answers"] != "192.0.2.10" || r.Details["resolver"] != server {
		t.Fatalf("expected answer, got %+v", r)
	}
	tc.Target = "missing.example"
	r = (&DNS{}).Probe(context.Background(), tc)
	if r.Success || r.ErrorClass != wire.ErrClassDNS {
		t.Fatalf("expected NXDOMAIN failure, got %+v", r)
	}
}

func TestEchoResultStatistics(t *testing.T) {
	rtts := []time.Duration{10 * time.Millisecond, 14 * time.Millisecond, 12 * time.Millisecond}
	r := echoResult(4, rtts, map[string]string{})
	if !r.Success || *r.LossPct != 25 || *r.LatencyMS != 12 || *r.JitterMS != 3 {
		t.Fatalf("unexpected stats: loss=%v latency=%v jitter=%v", *r.LossPct, *r.LatencyMS, *r.JitterMS)
	}
	if r.Details["min_ms"] != "10.000" || r.Details["max_ms"] != "14.000" {
		t.Fatalf("unexpected min/max: %v", r.Details)
	}
	r = echoResult(3, nil, map[string]string{})
	if r.Success || r.ErrorClass != wire.ErrClassLoss || *r.LossPct != 100 {
		t.Fatalf("expected total loss failure, got %+v", r)
	}
}

func TestEchoMarshalAndChecksum(t *testing.T) {
	msg := marshalEcho(0x1234, 7, []byte("nonce123"))
	if checksum(msg) != 0 {
		t.Fatal("checksum over a checksummed message must be zero")
	}
	msg[0] = icmpEchoReply
	id, seq, payload, ok := parseEchoReply(msg)
	if !ok || id != 0x1234 || seq != 7 || string(payload) != "nonce123" {
		t.Fatalf("round trip failed: %v %v %q %v", id, seq, payload, ok)
	}
}

func TestICMPLoopback(t *testing.T) {
	conn, _, err := listenICMP()
	if err != nil {
		t.Skipf("ICMP sockets unavailable here: %v", err)
	}
	conn.Close()
	tc := test(wire.KindICMP, "127.0.0.1")
	tc.Params.Count = 3
	r := (&ICMP{Resolver: net.DefaultResolver, Spacing: 10 * time.Millisecond}).Probe(context.Background(), tc)
	if !r.Success || *r.LossPct != 0 || r.Details["received"] != "3" {
		t.Fatalf("expected loopback replies, got %+v", r)
	}
}

func TestRunnerStampsIdentity(t *testing.T) {
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	r := NewRunner().With(wire.KindTCP, ProberFunc(func(context.Context, wire.Test) wire.Result {
		return wire.Result{Success: true}
	}))
	r.now = func() time.Time { return fixed }
	res := r.Run(context.Background(), test(wire.KindTCP, "x:1"))
	if res.TestID != "t1" || res.Kind != wire.KindTCP || res.Target != "x:1" || !res.Time.Equal(fixed) {
		t.Fatalf("identity not stamped: %+v", res)
	}
	res = r.Run(context.Background(), test("bogus", "x"))
	if res.Success || res.ErrorClass != wire.ErrClassConfig {
		t.Fatalf("unknown kind should be a config failure: %+v", res)
	}
}
