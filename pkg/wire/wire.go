// Package wire defines the versioned JSON contract shared by the PathLedger
// agent and controller. Both binaries import these types so the protocol
// cannot drift between them.
package wire

import (
	"time"
)

// APIVersion is the path segment for the current protocol version.
const APIVersion = "v1"

// ProbeKind identifies a measurement technique.
type ProbeKind string

// Supported probe kinds.
const (
	KindICMP ProbeKind = "icmp"
	KindDNS  ProbeKind = "dns"
	KindTCP  ProbeKind = "tcp"
	KindTLS  ProbeKind = "tls"
	KindHTTP ProbeKind = "http"
	// KindTraceroute maps the forward path hop by hop (Paris-style: the flow
	// identifier is held constant so ECMP load balancers keep every probe on
	// the same path).
	KindTraceroute ProbeKind = "traceroute"
)

// AllKinds lists every supported probe kind in a stable order.
var AllKinds = []ProbeKind{KindICMP, KindDNS, KindTCP, KindTLS, KindHTTP, KindTraceroute}

// Valid reports whether k is a supported probe kind.
func (k ProbeKind) Valid() bool {
	for _, known := range AllKinds {
		if k == known {
			return true
		}
	}
	return false
}

// ErrorClass groups probe failures so the controller can attribute faults
// without parsing free-text error messages.
type ErrorClass string

// Error classes reported by probes.
const (
	ErrClassNone       ErrorClass = ""
	ErrClassTimeout    ErrorClass = "timeout"
	ErrClassDNS        ErrorClass = "dns"
	ErrClassRefused    ErrorClass = "refused"
	ErrClassUnreach    ErrorClass = "unreachable"
	ErrClassTLS        ErrorClass = "tls"
	ErrClassHTTPStatus ErrorClass = "http_status"
	ErrClassLoss       ErrorClass = "loss"
	ErrClassConfig     ErrorClass = "config"
	ErrClassOther      ErrorClass = "other"
)

// TestParams holds optional, kind-specific settings for a test.
type TestParams struct {
	// Count is the number of ICMP echo requests per run (icmp), or the
	// number of probes sent per hop (traceroute).
	Count int `json:"count,omitempty"`
	// MaxHops bounds how far a traceroute walks (traceroute only).
	MaxHops int `json:"max_hops,omitempty"`
	// Resolver is an "ip:port" DNS server to query instead of the system
	// resolver (dns only).
	Resolver string `json:"resolver,omitempty"`
	// ExpectStatus, when set, is the exact HTTP status treated as success.
	// When zero, any 2xx or 3xx status is success (http only).
	ExpectStatus int `json:"expect_status,omitempty"`
}

// Test is one measurement an agent runs on a schedule.
type Test struct {
	ID              string     `json:"id"`
	Kind            ProbeKind  `json:"kind"`
	Target          string     `json:"target"`
	IntervalSeconds int        `json:"interval_seconds"`
	TimeoutMillis   int        `json:"timeout_millis"`
	Params          TestParams `json:"params"`
}

// Interval returns the scheduling interval as a duration.
func (t Test) Interval() time.Duration { return time.Duration(t.IntervalSeconds) * time.Second }

// Timeout returns the per-attempt timeout as a duration.
func (t Test) Timeout() time.Duration { return time.Duration(t.TimeoutMillis) * time.Millisecond }

// Plan is the full set of tests an agent should run.
type Plan struct {
	Tests       []Test    `json:"tests"`
	GeneratedAt time.Time `json:"generated_at"`
	// RotateCredential asks the agent to obtain a new credential
	// (POST /api/v1/agent/credential) and use it from then on.
	RotateCredential bool `json:"rotate_credential,omitempty"`
	// Update announces a signed release newer than nothing in particular;
	// the agent decides, after verifying the signature, whether to install.
	Update *UpdateHint `json:"update,omitempty"`
}

// UpdateHint names the release the controller is distributing.
type UpdateHint struct {
	Version string `json:"version"`
}

// CredentialResponse carries a rotated agent credential.
type CredentialResponse struct {
	AgentToken string `json:"agent_token"`
}

// Result is the outcome of a single probe run.
type Result struct {
	TestID     string            `json:"test_id"`
	Time       time.Time         `json:"time"`
	Kind       ProbeKind         `json:"kind"`
	Target     string            `json:"target"`
	Success    bool              `json:"success"`
	LatencyMS  *float64          `json:"latency_ms,omitempty"`
	LossPct    *float64          `json:"loss_pct,omitempty"`
	JitterMS   *float64          `json:"jitter_ms,omitempty"`
	ErrorClass ErrorClass        `json:"error_class,omitempty"`
	Error      string            `json:"error,omitempty"`
	Details    map[string]string `json:"details,omitempty"`
	// Hops is the forward path (traceroute only).
	Hops []Hop `json:"hops,omitempty"`
}

// Hop is one TTL step of a traceroute.
type Hop struct {
	TTL int `json:"ttl"`
	// Addr is the router that answered; empty when no probe was answered.
	Addr     string   `json:"addr,omitempty"`
	Sent     int      `json:"sent"`
	Received int      `json:"received"`
	AvgMS    *float64 `json:"avg_ms,omitempty"`
	MinMS    *float64 `json:"min_ms,omitempty"`
	MaxMS    *float64 `json:"max_ms,omitempty"`
	// Unreachable is set when the router reported the destination
	// unreachable: "host", "net", "admin" (filtered) or "other".
	Unreachable string `json:"unreachable,omitempty"`
	// Scope, ASN and ASName are filled in by the controller, not the agent.
	// Scope is "lan", "cgnat", "loopback", "public" or "other".
	Scope  string `json:"scope,omitempty"`
	ASN    int    `json:"asn,omitempty"`
	ASName string `json:"as_name,omitempty"`
}

// EnrollRequest exchanges a single-use enrolment token for agent credentials.
type EnrollRequest struct {
	Token        string `json:"token"`
	Hostname     string `json:"hostname"`
	AgentVersion string `json:"agent_version"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
}

// EnrollResponse carries the long-lived agent credential. AgentToken is shown
// exactly once; the controller stores only its hash.
type EnrollResponse struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
	TenantID   string `json:"tenant_id"`
	SiteID     string `json:"site_id"`
}

// ResultBatch is an upload of results from one agent.
type ResultBatch struct {
	Results []Result `json:"results"`
}

// IngestResponse reports how many results in a batch were stored. Rejected
// results (for example, for tests no longer assigned to the agent) are
// counted but never retried by the agent.
type IngestResponse struct {
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
}

// ErrorBody is the error envelope returned by every API endpoint.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is a machine-readable code plus a human-readable message.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Float returns a pointer to v, for populating optional metric fields.
func Float(v float64) *float64 { return &v }
