// Package probe implements PathLedger's network measurements. Every probe
// returns a wire.Result rather than an error: a failed measurement is data,
// not a program fault.
package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// Prober runs one measurement for a test.
type Prober interface {
	Probe(ctx context.Context, t wire.Test) wire.Result
}

// ProberFunc adapts a function to the Prober interface.
type ProberFunc func(ctx context.Context, t wire.Test) wire.Result

// Probe calls f.
func (f ProberFunc) Probe(ctx context.Context, t wire.Test) wire.Result { return f(ctx, t) }

// Resolver looks up IP addresses. *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Runner dispatches tests to the prober for their kind.
type Runner struct {
	probers map[wire.ProbeKind]Prober
	now     func() time.Time
}

// NewRunner returns a Runner with every built-in prober.
func NewRunner() *Runner {
	res := net.DefaultResolver
	return &Runner{
		probers: map[wire.ProbeKind]Prober{
			wire.KindICMP:       &ICMP{Resolver: res},
			wire.KindDNS:        &DNS{},
			wire.KindTCP:        &TCP{Resolver: res},
			wire.KindTLS:        &TLS{Resolver: res},
			wire.KindHTTP:       &HTTP{},
			wire.KindTraceroute: &Traceroute{Resolver: res},
		},
		now: time.Now,
	}
}

// With replaces the prober for a kind. It is intended for tests and for
// platforms that need an alternative implementation.
func (r *Runner) With(kind wire.ProbeKind, p Prober) *Runner {
	r.probers[kind] = p
	return r
}

// Run executes t and always returns a result stamped with the test identity
// and the time the run started.
func (r *Runner) Run(ctx context.Context, t wire.Test) wire.Result {
	start := r.now().UTC()
	p, ok := r.probers[t.Kind]
	var res wire.Result
	if !ok {
		res = failure(wire.ErrClassConfig, fmt.Errorf("no prober for kind %q", t.Kind))
	} else {
		res = p.Probe(ctx, t)
	}
	res.TestID, res.Kind, res.Target, res.Time = t.ID, t.Kind, t.Target, start
	return res
}

// failure builds an unsuccessful result.
func failure(class wire.ErrorClass, err error) wire.Result {
	msg := err.Error()
	if len(msg) > wire.MaxErrorLen {
		msg = msg[:wire.MaxErrorLen]
	}
	return wire.Result{Success: false, ErrorClass: class, Error: msg}
}

// classify maps a Go network error onto an ErrorClass.
func classify(err error) wire.ErrorClass {
	if err == nil {
		return wire.ErrClassNone
	}
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	var recordErr tls.RecordHeaderError
	var alertErr tls.AlertError
	switch {
	case errors.As(err, &dnsErr):
		if dnsErr.IsTimeout {
			return wire.ErrClassTimeout
		}
		return wire.ErrClassDNS
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return wire.ErrClassTimeout
	case errors.Is(err, syscall.ECONNREFUSED), isRefused(err):
		return wire.ErrClassRefused
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH), isUnreachable(err):
		return wire.ErrClassUnreach
	case errors.As(err, &certErr), errors.As(err, &unknownAuth), errors.As(err, &hostErr),
		errors.As(err, &invalidCert), errors.As(err, &recordErr), errors.As(err, &alertErr):
		return wire.ErrClassTLS
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return wire.ErrClassTimeout
	}
	return wire.ErrClassOther
}

// resolveOne resolves host to a single address, preferring IPv4 so results
// are comparable across runs. It also returns the time the lookup took.
func resolveOne(ctx context.Context, r Resolver, host string) (netip.Addr, time.Duration, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Unmap(), 0, nil
	}
	start := time.Now()
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	elapsed := time.Since(start)
	if err != nil {
		return netip.Addr{}, elapsed, err
	}
	for _, a := range addrs {
		if a.Unmap().Is4() {
			return a.Unmap(), elapsed, nil
		}
	}
	if len(addrs) == 0 {
		return netip.Addr{}, elapsed, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
	}
	return addrs[0], elapsed, nil
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func msString(d time.Duration) string { return fmt.Sprintf("%.3f", ms(d)) }

// splitHostPort splits a host:port target, removing IPv6 brackets.
func splitHostPort(target string) (host, port string, err error) {
	host, port, err = net.SplitHostPort(target)
	if err != nil {
		return "", "", err
	}
	return strings.Trim(host, "[]"), port, nil
}
