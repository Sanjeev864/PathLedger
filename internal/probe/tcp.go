package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// TCP measures TCP three-way-handshake time to host:port. DNS resolution is
// timed separately so a slow resolver is not mistaken for a slow path.
type TCP struct {
	Resolver Resolver
}

// Probe implements Prober.
func (p *TCP) Probe(ctx context.Context, t wire.Test) wire.Result {
	ctx, cancel := context.WithTimeout(ctx, t.Timeout())
	defer cancel()
	conn, details, connectTime, err := dialTCP(ctx, p.Resolver, t.Target)
	if err != nil {
		r := failure(classify(err), err)
		r.Details = details
		return r
	}
	_ = conn.Close()
	return wire.Result{Success: true, LatencyMS: wire.Float(ms(connectTime)), Details: details}
}

func dialTCP(ctx context.Context, res Resolver, target string) (net.Conn, map[string]string, time.Duration, error) {
	details := map[string]string{}
	host, port, err := splitHostPort(target)
	if err != nil {
		return nil, details, 0, err
	}
	addr, dnsTime, err := resolveOne(ctx, res, host)
	if dnsTime > 0 {
		details["dns_ms"] = msString(dnsTime)
	}
	if err != nil {
		return nil, details, 0, err
	}
	details["remote_ip"] = addr.String()
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil, details, 0, fmt.Errorf("invalid port %q", port)
	}
	var d net.Dialer
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(addr.String(), strconv.Itoa(portNum)))
	elapsed := time.Since(start)
	if err != nil {
		return nil, details, elapsed, err
	}
	details["connect_ms"] = msString(elapsed)
	return conn, details, elapsed, nil
}

// TLS measures TCP connect plus TLS handshake time and reports the negotiated
// version and how many days remain on the server certificate.
type TLS struct {
	Resolver Resolver
	// RootCAs overrides the system trust store; nil uses the system pool.
	RootCAs *x509.CertPool
	now     func() time.Time
}

// Probe implements Prober.
func (p *TLS) Probe(ctx context.Context, t wire.Test) wire.Result {
	ctx, cancel := context.WithTimeout(ctx, t.Timeout())
	defer cancel()
	conn, details, _, err := dialTCP(ctx, p.Resolver, t.Target)
	if err != nil {
		r := failure(classify(err), err)
		r.Details = details
		return r
	}
	defer conn.Close()
	host, _, _ := splitHostPort(t.Target)
	tc := tls.Client(conn, &tls.Config{ServerName: host, RootCAs: p.RootCAs, MinVersion: tls.VersionTLS12})
	start := time.Now()
	err = tc.HandshakeContext(ctx)
	hs := time.Since(start)
	if err != nil {
		r := failure(classify(err), fmt.Errorf("tls handshake: %w", err))
		if r.ErrorClass == wire.ErrClassOther {
			r.ErrorClass = wire.ErrClassTLS
		}
		r.Details = details
		return r
	}
	details["handshake_ms"] = msString(hs)
	state := tc.ConnectionState()
	details["tls_version"] = tls.VersionName(state.Version)
	details["cipher"] = tls.CipherSuiteName(state.CipherSuite)
	if len(state.PeerCertificates) > 0 {
		now := time.Now
		if p.now != nil {
			now = p.now
		}
		leaf := state.PeerCertificates[0]
		details["cert_days_left"] = fmt.Sprintf("%d", int(leaf.NotAfter.Sub(now()).Hours()/24))
		details["cert_issuer"] = leaf.Issuer.CommonName
	}
	return wire.Result{Success: true, LatencyMS: wire.Float(ms(hs)), Details: details}
}
