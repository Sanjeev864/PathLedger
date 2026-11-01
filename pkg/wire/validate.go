package wire

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// Limits that keep a misconfigured plan from turning an agent into a traffic
// generator or from hanging on a single probe.
const (
	MinIntervalSeconds = 10
	MaxIntervalSeconds = 3600
	MinTimeoutMillis   = 100
	MaxTimeoutMillis   = 30_000
	MaxICMPCount       = 20
	DefaultICMPCount   = 5
	MaxTraceHops       = 32
	DefaultTraceHops   = 30
	MaxTraceCount      = 5
	DefaultTraceCount  = 3
	// MinTraceInterval keeps path discovery to at most once a minute.
	MinTraceInterval = 60
	MaxTargetLength    = 512
	MaxResultsPerBatch = 500
	MaxDetailEntries   = 32
	MaxDetailValueLen  = 512
	MaxErrorLen        = 1024
)

// ValidationError describes why a value was rejected.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// IsValidationError reports whether err is (or wraps) a ValidationError.
func IsValidationError(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

// Validate checks every field of a test definition. It does not check ID,
// which is assigned by the controller.
func (t Test) Validate() error {
	if !t.Kind.Valid() {
		return invalid("kind", "unsupported probe kind %q", t.Kind)
	}
	if t.IntervalSeconds < MinIntervalSeconds || t.IntervalSeconds > MaxIntervalSeconds {
		return invalid("interval_seconds", "must be between %d and %d", MinIntervalSeconds, MaxIntervalSeconds)
	}
	if t.TimeoutMillis < MinTimeoutMillis || t.TimeoutMillis > MaxTimeoutMillis {
		return invalid("timeout_millis", "must be between %d and %d", MinTimeoutMillis, MaxTimeoutMillis)
	}
	if t.TimeoutMillis >= t.IntervalSeconds*1000 {
		return invalid("timeout_millis", "must be shorter than the interval")
	}
	if t.Kind == KindTraceroute {
		if t.IntervalSeconds < MinTraceInterval {
			return invalid("interval_seconds", "traceroute tests run at most once every %d seconds", MinTraceInterval)
		}
		hops, count := t.Params.TraceHops(), t.Params.TraceCount()
		// timeout_millis is the wait per probe; the worst case must fit the interval.
		if hops*count*t.TimeoutMillis >= t.IntervalSeconds*1000 {
			return invalid("timeout_millis", "max_hops × count × timeout_millis must be shorter than the interval")
		}
	}
	if err := ValidateTarget(t.Kind, t.Target); err != nil {
		return err
	}
	return t.Params.validate(t.Kind)
}

// TraceHops returns the effective traceroute hop limit.
func (p TestParams) TraceHops() int {
	if p.MaxHops == 0 {
		return DefaultTraceHops
	}
	return p.MaxHops
}

// TraceCount returns the effective probes per traceroute hop.
func (p TestParams) TraceCount() int {
	if p.Count == 0 {
		return DefaultTraceCount
	}
	return p.Count
}

func (p TestParams) validate(kind ProbeKind) error {
	switch {
	case p.Count != 0 && kind != KindICMP && kind != KindTraceroute:
		return invalid("params.count", "only applies to icmp and traceroute tests")
	case kind == KindTraceroute && (p.Count < 0 || p.Count > MaxTraceCount):
		return invalid("params.count", "must be between 1 and %d", MaxTraceCount)
	case p.Count < 0 || p.Count > MaxICMPCount:
		return invalid("params.count", "must be between 1 and %d", MaxICMPCount)
	}
	if p.MaxHops != 0 && (kind != KindTraceroute || p.MaxHops < 1 || p.MaxHops > MaxTraceHops) {
		return invalid("params.max_hops", "applies only to traceroute tests and must be between 1 and %d", MaxTraceHops)
	}
	if p.Resolver != "" {
		if kind != KindDNS {
			return invalid("params.resolver", "only applies to dns tests")
		}
		ap, err := netip.ParseAddrPort(p.Resolver)
		if err != nil || ap.Port() == 0 {
			return invalid("params.resolver", "must be an ip:port address such as 1.1.1.1:53")
		}
	}
	if p.ExpectStatus != 0 {
		if kind != KindHTTP {
			return invalid("params.expect_status", "only applies to http tests")
		}
		if p.ExpectStatus < 100 || p.ExpectStatus > 599 {
			return invalid("params.expect_status", "must be a valid HTTP status code")
		}
	}
	return nil
}

// ValidateTarget checks that target is well-formed for the probe kind:
// a host for icmp and dns, host:port for tcp and tls, and an http(s) URL for http.
func ValidateTarget(kind ProbeKind, target string) error {
	if target == "" {
		return invalid("target", "is required")
	}
	if len(target) > MaxTargetLength {
		return invalid("target", "must be at most %d characters", MaxTargetLength)
	}
	switch kind {
	case KindICMP, KindTraceroute:
		if !validHost(target) {
			return invalid("target", "must be a hostname or IP address")
		}
	case KindDNS:
		if _, err := netip.ParseAddr(target); err == nil || !validHostname(target) {
			return invalid("target", "must be a domain name to resolve")
		}
	case KindTCP, KindTLS:
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			return invalid("target", "must be host:port")
		}
		if !validHost(host) {
			return invalid("target", "has an invalid host")
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return invalid("target", "has an invalid port")
		}
	case KindHTTP:
		u, err := url.Parse(target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return invalid("target", "must be an http:// or https:// URL")
		}
		if u.User != nil {
			return invalid("target", "must not contain credentials")
		}
		if !validHost(u.Hostname()) {
			return invalid("target", "has an invalid host")
		}
	default:
		return invalid("kind", "unsupported probe kind %q", kind)
	}
	return nil
}

func validHost(h string) bool {
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	return validHostname(h)
}

// validHostname applies RFC 1123 label rules.
func validHostname(h string) bool {
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// Validate checks a result received from an agent.
func (r Result) Validate() error {
	if r.TestID == "" {
		return invalid("test_id", "is required")
	}
	if r.Time.IsZero() {
		return invalid("time", "is required")
	}
	if !r.Kind.Valid() {
		return invalid("kind", "unsupported probe kind %q", r.Kind)
	}
	if len(r.Target) > MaxTargetLength {
		return invalid("target", "is too long")
	}
	for name, v := range map[string]*float64{"latency_ms": r.LatencyMS, "jitter_ms": r.JitterMS} {
		if v != nil && (*v < 0 || *v > 600_000) {
			return invalid(name, "is out of range")
		}
	}
	if r.LossPct != nil && (*r.LossPct < 0 || *r.LossPct > 100) {
		return invalid("loss_pct", "must be between 0 and 100")
	}
	if len(r.Error) > MaxErrorLen {
		return invalid("error", "is too long")
	}
	if len(r.Details) > MaxDetailEntries {
		return invalid("details", "has too many entries")
	}
	for k, v := range r.Details {
		if len(k) > 64 || len(v) > MaxDetailValueLen {
			return invalid("details", "entry %q is too long", k)
		}
	}
	if len(r.Hops) > MaxTraceHops {
		return invalid("hops", "at most %d hops", MaxTraceHops)
	}
	for _, h := range r.Hops {
		if h.TTL < 1 || h.TTL > MaxTraceHops || h.Sent < 0 || h.Received < 0 || h.Received > h.Sent || len(h.Addr) > 64 || len(h.Unreachable) > 16 {
			return invalid("hops", "hop %d is malformed", h.TTL)
		}
	}
	return nil
}
