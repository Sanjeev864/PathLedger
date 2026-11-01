package probe

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// sysPinger sends ICMP echo requests through an operating-system service
// instead of a socket (Windows). systemPinger returns nil where sockets are
// used instead.
type sysPinger interface {
	ping(dst netip.Addr, ttl int, timeout time.Duration, payload []byte) (sysReply, error)
}

type replyKind int

const (
	replyEcho replyKind = iota + 1
	replyTTLExpired
	replyUnreachable
)

type sysReply struct {
	kind        replyKind
	from        netip.Addr
	rtt         time.Duration
	unreachable string
}

// errSysTimeout is a single request that got no answer.
var errSysTimeout = errors.New("request timed out")

// sysEcho runs an echo train through sp.
func sysEcho(ctx context.Context, sp sysPinger, dst netip.Addr, count int, spacing, timeout time.Duration, details map[string]string) wire.Result {
	details["socket"] = "system"
	payload := make([]byte, 32)
	var rtts []time.Duration
	for i := 0; i < count && ctx.Err() == nil; i++ {
		sentAt := time.Now()
		rep, err := sp.ping(dst, 128, timeout, payload)
		switch {
		case errors.Is(err, errSysTimeout):
		case err != nil:
			r := failure(wire.ErrClassConfig, err)
			r.Details = details
			return r
		case rep.kind == replyEcho:
			rtts = append(rtts, rep.rtt)
		case rep.kind == replyUnreachable:
			r := failure(wire.ErrClassUnreach, errors.New(rep.from.String()+" reported destination unreachable ("+rep.unreachable+")"))
			r.Details = details
			return r
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

// sysHop adapts sp to the tracer's per-probe step. Windows traces with ICMP
// echo requests (like tracert) rather than UDP.
func sysHop(sp sysPinger, dst netip.Addr) func(context.Context, int, time.Duration) (traceReply, error) {
	payload := make([]byte, 32)
	return func(ctx context.Context, ttl int, wait time.Duration) (traceReply, error) {
		if d, ok := ctx.Deadline(); ok && time.Until(d) < wait {
			wait = time.Until(d)
		}
		rep, err := sp.ping(dst, ttl, wait, payload)
		if err != nil {
			return traceReply{}, err
		}
		tr := traceReply{from: rep.from, rtt: rep.rtt}
		switch rep.kind {
		case replyEcho:
			tr.atDest = true
		case replyUnreachable:
			if rep.from == dst {
				tr.atDest = true
			} else {
				tr.unreachable = rep.unreachable
			}
		}
		return tr, nil
	}
}
