package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// maxHTTPBody bounds how much of a response body is read, so a probe can
// never download a large file over a customer's metered link.
const maxHTTPBody = 256 << 10

// HTTP measures a single GET request with a per-phase breakdown (DNS,
// connect, TLS, time to first byte). Redirects are not followed: the probe
// measures the URL the customer configured.
type HTTP struct {
	// RootCAs overrides the system trust store; nil uses the system pool.
	RootCAs *x509.CertPool
	// UserAgent is sent with each request.
	UserAgent string
}

// Probe implements Prober.
func (p *HTTP) Probe(ctx context.Context, t wire.Test) wire.Result {
	ctx, cancel := context.WithTimeout(ctx, t.Timeout())
	defer cancel()

	// Trace callbacks can run on transport goroutines (and concurrently for
	// dual-stack dials), so every access to the shared state is locked.
	var (
		mu                            sync.Mutex
		dnsStart, connStart, tlsStart time.Time
		reqStart                      time.Time
		details                       = map[string]string{}
	)
	set := func(k, v string) { mu.Lock(); details[k] = v; mu.Unlock() }
	mark := func(t *time.Time) { mu.Lock(); *t = time.Now(); mu.Unlock() }
	since := func(t *time.Time) string { mu.Lock(); defer mu.Unlock(); return msString(time.Since(*t)) }
	// snapshot copies details so a late callback from an abandoned dial
	// cannot mutate the map after the result is returned.
	snapshot := func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]string, len(details))
		for k, v := range details {
			out[k] = v
		}
		return out
	}
	trace := &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) { mark(&dnsStart) },
		DNSDone:      func(httptrace.DNSDoneInfo) { set("dns_ms", since(&dnsStart)) },
		ConnectStart: func(string, string) { mark(&connStart) },
		ConnectDone: func(_, addr string, err error) {
			if err == nil {
				set("connect_ms", since(&connStart))
				if host, _, e := net.SplitHostPort(addr); e == nil {
					set("remote_ip", host)
				}
			}
		},
		TLSHandshakeStart: func() { mark(&tlsStart) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				set("tls_ms", since(&tlsStart))
			}
		},
		GotFirstResponseByte: func() { set("ttfb_ms", since(&reqStart)) },
	}

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, t.Target, nil)
	if err != nil {
		return failure(wire.ErrClassConfig, err)
	}
	ua := p.UserAgent
	if ua == "" {
		ua = "PathLedger-Agent"
	}
	req.Header.Set("User-Agent", ua)

	transport := &http.Transport{
		Proxy:               nil, // measure the direct path, never a configured proxy
		DisableKeepAlives:   true,
		TLSClientConfig:     &tls.Config{RootCAs: p.RootCAs, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: t.Timeout(),
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	mark(&reqStart)
	resp, err := client.Do(req)
	if err != nil {
		r := failure(classify(err), err)
		r.Details = snapshot()
		return r
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxHTTPBody))
	mu.Lock()
	total := time.Since(reqStart)
	mu.Unlock()
	set("status", strconv.Itoa(resp.StatusCode))
	set("bytes", strconv.FormatInt(n, 10))
	if err != nil {
		r := failure(classify(err), fmt.Errorf("reading body: %w", err))
		r.Details = snapshot()
		return r
	}

	ok := resp.StatusCode >= 200 && resp.StatusCode < 400
	if want := t.Params.ExpectStatus; want != 0 {
		ok = resp.StatusCode == want
	}
	if !ok {
		r := failure(wire.ErrClassHTTPStatus, fmt.Errorf("unexpected status %d", resp.StatusCode))
		r.LatencyMS = wire.Float(ms(total))
		r.Details = snapshot()
		return r
	}
	return wire.Result{Success: true, LatencyMS: wire.Float(ms(total)), Details: snapshot()}
}
