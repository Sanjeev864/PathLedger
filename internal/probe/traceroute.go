package probe

import (
	"context"
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
	// traceDstPort is the classic traceroute base port. It is held constant
	// (Paris traceroute): every probe of a run shares one 5-tuple, so ECMP
	// load balancers hash them onto the same path and the result is a real
	// path rather than a blend of several.
	traceDstPort = 33434
	// tracePayloadBase is the smallest probe payload. Each probe gets a
	// distinct payload length, which routers echo back in the quoted UDP
	// header; the length (unlike ports) does not affect ECMP hashing.
	tracePayloadBase = 16
	// traceSilentStop ends a run after this many consecutive fully silent
	// hops, which almost always means a firewall has eaten everything beyond.
	traceSilentStop = 5

	icmpDestUnreachable = 3
	icmpTimeExceeded    = 11
)

// Traceroute discovers the forward path with UDP probes and ICMP replies.
// It needs a raw ICMP socket (root or CAP_NET_RAW, which Docker grants by
// default and the systemd unit grants explicitly). IPv4 only for now.
type Traceroute struct {
	Resolver Resolver
	// ListenICMP opens the raw socket that receives router replies.
	ListenICMP func() (net.PacketConn, error)
}

// tracePacketConn is the UDP side; abstracted so TTL setting is testable.
type tracePacketConn interface {
	net.PacketConn
	SetTTL(ttl int) error
}

// Probe implements Prober.
func (p *Traceroute) Probe(ctx context.Context, t wire.Test) wire.Result {
	details := map[string]string{}
	dst, dnsTime, err := resolveOne(ctx, p.Resolver, t.Target)
	if dnsTime > 0 {
		details["dns_ms"] = msString(dnsTime)
	}
	if err != nil {
		r := failure(classify(err), err)
		r.Details = details
		return r
	}
	if !dst.Is4() {
		return failure(wire.ErrClassConfig, errors.New("traceroute over IPv6 is not supported yet"))
	}
	details["remote_ip"] = dst.String()

	listen := p.ListenICMP
	if listen == nil {
		listen = func() (net.PacketConn, error) { return net.ListenPacket("ip4:icmp", "0.0.0.0") }
	}
	icmp, err := listen()
	if err != nil {
		return failure(wire.ErrClassConfig, fmt.Errorf("traceroute needs a raw ICMP socket (root or CAP_NET_RAW): %w", err))
	}
	defer icmp.Close()
	udp, err := listenTraceUDP()
	if err != nil {
		return failure(wire.ErrClassConfig, err)
	}
	defer udp.Close()

	tr := &tracer{icmp: icmp, udp: udp, dst: dst, srcPort: uint16(udp.LocalAddr().(*net.UDPAddr).Port), buf: make([]byte, 1500)}
	hops, reached := tr.run(ctx, t.Params.TraceHops(), t.Params.TraceCount(), t.Timeout())
	return traceResult(hops, reached, details)
}

// traceResult summarises a path. Success means the destination answered;
// latency and loss describe the final hop.
func traceResult(hops []wire.Hop, reached bool, details map[string]string) wire.Result {
	details["hops"] = strconv.Itoa(len(hops))
	if !reached {
		r := failure(wire.ErrClassUnreach, errors.New("destination did not answer"))
		if n := len(hops); n > 0 && hops[n-1].Unreachable != "" {
			r.Error = fmt.Sprintf("%s reported destination unreachable (%s)", hops[n-1].Addr, hops[n-1].Unreachable)
		}
		r.Details, r.Hops = details, hops
		return r
	}
	last := hops[len(hops)-1]
	loss := 100 * float64(last.Sent-last.Received) / float64(last.Sent)
	return wire.Result{Success: true, LatencyMS: last.AvgMS, LossPct: wire.Float(loss), Details: details, Hops: hops}
}

type tracer struct {
	icmp    net.PacketConn
	udp     tracePacketConn
	dst     netip.Addr
	srcPort uint16
	buf     []byte
	seq     int
}

type traceReply struct {
	from        netip.Addr
	rtt         time.Duration
	atDest      bool   // port unreachable from the destination itself
	unreachable string // other destination-unreachable codes
}

func (tr *tracer) run(ctx context.Context, maxHops, perHop int, wait time.Duration) ([]wire.Hop, bool) {
	var hops []wire.Hop
	silent := 0
	for ttl := 1; ttl <= maxHops && ctx.Err() == nil; ttl++ {
		hop := wire.Hop{TTL: ttl}
		var rtts []time.Duration
		stop := false
		for q := 0; q < perHop && ctx.Err() == nil; q++ {
			hop.Sent++
			rep, err := tr.probe(ctx, ttl, wait)
			if err != nil {
				continue
			}
			hop.Received++
			rtts = append(rtts, rep.rtt)
			if hop.Addr == "" {
				hop.Addr = rep.from.String()
			}
			if rep.atDest {
				stop = true
			}
			if rep.unreachable != "" {
				hop.Unreachable, stop = rep.unreachable, true
			}
		}
		if len(rtts) > 0 {
			sum, lo, hi := time.Duration(0), rtts[0], rtts[0]
			for _, d := range rtts {
				sum, lo, hi = sum+d, min(lo, d), max(hi, d)
			}
			hop.AvgMS = wire.Float(math.Round(ms(sum)/float64(len(rtts))*1000) / 1000)
			hop.MinMS, hop.MaxMS = wire.Float(ms(lo)), wire.Float(ms(hi))
			silent = 0
		} else {
			silent++
		}
		hops = append(hops, hop)
		if stop {
			return hops, hop.Unreachable == "" && hop.Received > 0
		}
		if silent >= traceSilentStop {
			break
		}
	}
	return hops, false
}

// probe sends one UDP datagram with the given TTL and waits for the router
// or destination reply that quotes it.
func (tr *tracer) probe(ctx context.Context, ttl int, wait time.Duration) (traceReply, error) {
	tr.seq++
	payloadLen := tracePayloadBase + tr.seq
	if err := tr.udp.SetTTL(ttl); err != nil {
		return traceReply{}, err
	}
	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := tr.icmp.SetReadDeadline(deadline); err != nil {
		return traceReply{}, err
	}
	start := time.Now()
	dst := &net.UDPAddr{IP: tr.dst.AsSlice(), Port: traceDstPort}
	if _, err := tr.udp.WriteTo(make([]byte, payloadLen), dst); err != nil {
		return traceReply{}, err
	}
	for {
		n, from, err := tr.icmp.ReadFrom(tr.buf)
		if err != nil {
			return traceReply{}, err
		}
		q, ok := parseTraceICMP(tr.buf[:n])
		if !ok || q.dst != tr.dst || q.srcPort != tr.srcPort || q.dstPort != traceDstPort || int(q.udpLen) != 8+payloadLen {
			continue
		}
		ip, _ := netip.AddrFromSlice(from.(*net.IPAddr).IP)
		rep := traceReply{from: ip.Unmap(), rtt: time.Since(start)}
		if q.typ == icmpDestUnreachable {
			if q.code == 3 && rep.from == tr.dst {
				rep.atDest = true
			} else {
				rep.unreachable = unreachableReason(q.code)
			}
		}
		return rep, nil
	}
}

// quoted is what an ICMP error tells us about the probe that triggered it.
type quoted struct {
	typ, code        byte
	dst              netip.Addr
	srcPort, dstPort uint16
	udpLen           uint16
}

// parseTraceICMP parses an ICMP time-exceeded or destination-unreachable
// message (outer IP header already stripped) and the IPv4 + UDP headers it
// quotes.
func parseTraceICMP(b []byte) (quoted, bool) {
	if len(b) < 8+20+8 || (b[0] != icmpTimeExceeded && b[0] != icmpDestUnreachable) {
		return quoted{}, false
	}
	inner := b[8:]
	if inner[0]>>4 != 4 {
		return quoted{}, false
	}
	ihl := int(inner[0]&0x0f) * 4
	if ihl < 20 || len(inner) < ihl+8 || inner[9] != 17 { // 17 = UDP
		return quoted{}, false
	}
	dst, _ := netip.AddrFromSlice(inner[16:20])
	udp := inner[ihl:]
	return quoted{
		typ: b[0], code: b[1], dst: dst,
		srcPort: binary.BigEndian.Uint16(udp[0:]),
		dstPort: binary.BigEndian.Uint16(udp[2:]),
		udpLen:  binary.BigEndian.Uint16(udp[4:]),
	}, true
}

func unreachableReason(code byte) string {
	switch code {
	case 0, 6, 9:
		return "net"
	case 1, 7, 10:
		return "host"
	case 13:
		return "admin"
	default:
		return "other"
	}
}
