package probe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

const (
	icmpEchoRequest = 8
	icmpEchoReply   = 0
	icmpHeaderLen   = 8
	nonceLen        = 8
)

// ICMP sends a short train of echo requests and reports average round-trip
// time, packet loss and jitter (mean absolute difference between consecutive
// RTTs, in the spirit of RFC 3550).
//
// It prefers unprivileged datagram ping sockets (Linux
// net.ipv4.ping_group_range, macOS) and falls back to raw sockets, which
// need root or CAP_NET_RAW. On Windows it uses the system ICMP helper, which
// needs no administrator rights. IPv4 only for now.
type ICMP struct {
	Resolver Resolver
	// Spacing is the gap between echo requests; defaults to 200ms.
	Spacing time.Duration
	// Listen opens the ICMP socket; defaults to listenICMP. Tests override it.
	Listen func() (conn net.PacketConn, raw bool, err error)
}

// Probe implements Prober.
func (p *ICMP) Probe(ctx context.Context, t wire.Test) wire.Result {
	details := map[string]string{}
	addr, dnsTime, err := resolveOne(ctx, p.Resolver, t.Target)
	if dnsTime > 0 {
		details["dns_ms"] = msString(dnsTime)
	}
	if err != nil {
		r := failure(classify(err), err)
		r.Details = details
		return r
	}
	if !addr.Is4() {
		return failure(wire.ErrClassConfig, errors.New("icmp over IPv6 is not supported yet"))
	}
	details["remote_ip"] = addr.String()

	count := t.Params.Count
	if count == 0 {
		count = wire.DefaultICMPCount
	}
	spacing := p.Spacing
	if spacing == 0 {
		spacing = 200 * time.Millisecond
	}

	listen := p.Listen
	if listen == nil {
		if sp := systemPinger(); sp != nil {
			return sysEcho(ctx, sp, addr, count, spacing, t.Timeout(), details)
		}
		listen = listenICMP
	}
	conn, raw, err := listen()
	if err != nil {
		return failure(wire.ErrClassConfig, err)
	}
	defer conn.Close()
	details["socket"] = map[bool]string{true: "raw", false: "datagram"}[raw]

	s, err := newEchoSession(conn, raw, addr)
	if err != nil {
		return failure(wire.ErrClassOther, err)
	}
	var rtts []time.Duration
	for i := 0; i < count; i++ {
		if ctx.Err() != nil {
			break
		}
		sentAt := time.Now()
		rtt, err := s.ping(ctx, uint16(i), t.Timeout())
		if err != nil && !isTimeout(err) {
			r := failure(classify(err), err)
			r.Details = details
			return r
		}
		if err == nil {
			rtts = append(rtts, rtt)
		}
		if i < count-1 {
			if wait := spacing - time.Since(sentAt); wait > 0 {
				select {
				case <-ctx.Done():
				case <-time.After(wait):
				}
			}
		}
	}
	return echoResult(count, rtts, details)
}

// echoResult turns raw RTT samples into a result. It is separate from Probe
// so the statistics can be tested without sockets.
func echoResult(sent int, rtts []time.Duration, details map[string]string) wire.Result {
	received := len(rtts)
	details["sent"] = strconv.Itoa(sent)
	details["received"] = strconv.Itoa(received)
	loss := 100 * float64(sent-received) / float64(sent)
	if received == 0 {
		r := failure(wire.ErrClassLoss, fmt.Errorf("no replies to %d echo requests", sent))
		r.LossPct = wire.Float(loss)
		r.Details = details
		return r
	}
	var sum time.Duration
	minRTT, maxRTT := rtts[0], rtts[0]
	for _, d := range rtts {
		sum += d
		minRTT, maxRTT = min(minRTT, d), max(maxRTT, d)
	}
	var jitter float64
	if received > 1 {
		for i := 1; i < received; i++ {
			jitter += math.Abs(ms(rtts[i]) - ms(rtts[i-1]))
		}
		jitter /= float64(received - 1)
	}
	details["min_ms"] = msString(minRTT)
	details["max_ms"] = msString(maxRTT)
	return wire.Result{
		Success:   true,
		LatencyMS: wire.Float(ms(sum) / float64(received)),
		LossPct:   wire.Float(loss),
		JitterMS:  wire.Float(math.Round(jitter*1000) / 1000),
		Details:   details,
	}
}

type echoSession struct {
	conn  net.PacketConn
	raw   bool
	dst   net.Addr
	ip    netip.Addr
	id    uint16
	nonce [nonceLen]byte
	buf   []byte
}

func newEchoSession(conn net.PacketConn, raw bool, ip netip.Addr) (*echoSession, error) {
	s := &echoSession{conn: conn, raw: raw, ip: ip, buf: make([]byte, 1500)}
	if raw {
		s.dst = &net.IPAddr{IP: ip.AsSlice()}
	} else {
		s.dst = &net.UDPAddr{IP: ip.AsSlice()}
	}
	var idb [2]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return nil, err
	}
	s.id = binary.BigEndian.Uint16(idb[:])
	if _, err := rand.Read(s.nonce[:]); err != nil {
		return nil, err
	}
	return s, nil
}

// ping sends one echo request and waits for the matching reply.
func (s *echoSession) ping(ctx context.Context, seq uint16, timeout time.Duration) (time.Duration, error) {
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := s.conn.SetDeadline(deadline); err != nil {
		return 0, err
	}
	msg := marshalEcho(s.id, seq, s.nonce[:])
	start := time.Now()
	if _, err := s.conn.WriteTo(msg, s.dst); err != nil {
		return 0, err
	}
	for {
		n, from, err := s.conn.ReadFrom(s.buf)
		if err != nil {
			return 0, err
		}
		if !s.fromTarget(from) {
			continue
		}
		msg := s.buf[:n]
		if !s.raw {
			msg = stripIPv4Header(msg)
		}
		id, rseq, payload, ok := parseEchoReply(msg)
		// Datagram ping sockets rewrite the ID, and the kernel already
		// demultiplexes replies to us, so only raw sockets check it.
		if !ok || rseq != seq || (s.raw && id != s.id) || !bytes.Equal(payload, s.nonce[:]) {
			continue
		}
		return time.Since(start), nil
	}
}

func (s *echoSession) fromTarget(a net.Addr) bool {
	var ip net.IP
	switch v := a.(type) {
	case *net.IPAddr:
		ip = v.IP
	case *net.UDPAddr:
		ip = v.IP
	default:
		return false
	}
	got, ok := netip.AddrFromSlice(ip)
	return ok && got.Unmap() == s.ip
}

func marshalEcho(id, seq uint16, payload []byte) []byte {
	b := make([]byte, icmpHeaderLen+len(payload))
	b[0] = icmpEchoRequest
	binary.BigEndian.PutUint16(b[4:], id)
	binary.BigEndian.PutUint16(b[6:], seq)
	copy(b[icmpHeaderLen:], payload)
	binary.BigEndian.PutUint16(b[2:], checksum(b))
	return b
}

// stripIPv4Header removes an IPv4 header if present. macOS datagram ping
// sockets deliver replies with the IP header; Linux ones do not. An ICMP
// message never starts with 0x4_ (no ICMP type is 64-79 here), so the
// check is unambiguous.
func stripIPv4Header(b []byte) []byte {
	if len(b) >= 20 && b[0]>>4 == 4 {
		if ihl := int(b[0]&0x0f) * 4; ihl >= 20 && len(b) >= ihl {
			return b[ihl:]
		}
	}
	return b
}

func parseEchoReply(b []byte) (id, seq uint16, payload []byte, ok bool) {
	if len(b) < icmpHeaderLen || b[0] != icmpEchoReply || b[1] != 0 {
		return 0, 0, nil, false
	}
	return binary.BigEndian.Uint16(b[4:]), binary.BigEndian.Uint16(b[6:]), b[icmpHeaderLen:], true
}

// checksum is the RFC 1071 Internet checksum.
func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
