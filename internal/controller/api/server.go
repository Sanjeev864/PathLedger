// Package api is the controller's HTTP interface: the agent API used by
// branch agents and the management API used by the dashboard and CLI tools.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/releases"
	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/internal/controller/webui"
	"github.com/sanjeev864/pathledger/internal/metrics"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

// Store is the persistence the API needs. *store.Store implements it.
type Store interface {
	Ping(ctx context.Context) error

	AuthenticateAPIKey(ctx context.Context, token string) (tenantID, keyID string, err error)
	AuthenticateAgent(ctx context.Context, token string) (store.AgentIdentity, error)

	EnrollAgent(ctx context.Context, req wire.EnrollRequest) (wire.EnrollResponse, error)
	PlanFor(ctx context.Context, tenantID, siteID string) ([]wire.Test, error)
	InsertResults(ctx context.Context, agent store.AgentIdentity, results []wire.Result) (accepted, rejected int, err error)

	CreateSite(ctx context.Context, tenantID, name, location string) (store.Site, error)
	ListSites(ctx context.Context, tenantID string) ([]store.Site, error)
	GetSite(ctx context.Context, tenantID, siteID string) (store.Site, error)
	CreateEnrollmentToken(ctx context.Context, tenantID, siteID string, ttl time.Duration) (store.EnrollmentToken, string, error)
	ListAgents(ctx context.Context, tenantID string) ([]store.Agent, error)
	RevokeAgent(ctx context.Context, tenantID, agentID string) error
	RotateAgentToken(ctx context.Context, agentID string) (string, error)
	RequestAgentRotation(ctx context.Context, tenantID, agentID string) error
	SetAgentVersion(ctx context.Context, agentID, version string) error
	CreateTest(ctx context.Context, tenantID string, siteID *string, t wire.Test) (store.Test, error)
	ListTests(ctx context.Context, tenantID string) ([]store.Test, error)
	DeleteTest(ctx context.Context, tenantID, testID string) error
	QueryResults(ctx context.Context, tenantID string, f store.ResultFilter) ([]store.StoredResult, error)
	ListEvents(ctx context.Context, tenantID string, f store.EventFilter) ([]store.Event, error)
	GetEvent(ctx context.Context, tenantID, id string) (store.Event, error)
	CreateChannel(ctx context.Context, c store.Channel) (store.Channel, error)
	ListChannels(ctx context.Context, tenantID string) ([]store.Channel, error)
	GetChannel(ctx context.Context, tenantID, id string) (store.Channel, error)
	DeleteChannel(ctx context.Context, tenantID, id string) error
	EnqueueDeliveries(ctx context.Context, tenantID, channelID string, payload any) (int, error)
	ListDeliveries(ctx context.Context, tenantID string, limit int) ([]store.DeliveryStatus, error)
	GetTest(ctx context.Context, tenantID, testID string) (store.Test, error)
	UpdateTest(ctx context.Context, tenantID string, t store.Test) (store.Test, error)
	CreateAPIKey(ctx context.Context, tenantID, name string) (store.APIKey, string, error)
	ListAPIKeys(ctx context.Context, tenantID string) ([]store.APIKey, error)
	RevokeAPIKey(ctx context.Context, tenantID, keyID string) error
	Series(ctx context.Context, tenantID, siteID, testID string, from, to time.Time, step time.Duration) ([]store.SeriesPoint, error)
	SLAReportData(ctx context.Context, tenantID string, from, to time.Time) (store.ReportData, error)

	CreateUser(ctx context.Context, email, name, hash string) (store.User, error)
	UserByEmail(ctx context.Context, email string) (store.User, error)
	UserByID(ctx context.Context, id string) (store.User, error)
	RecordLogin(ctx context.Context, userID string) error
	SetPassword(ctx context.Context, userID, hash string) error
	Memberships(ctx context.Context, userID string) ([]store.Membership, error)
	Members(ctx context.Context, tenantID string) ([]store.Member, error)
	MemberRole(ctx context.Context, tenantID, userID string) (store.Role, error)
	SetMemberRole(ctx context.Context, tenantID, userID string, role store.Role) error
	RemoveMember(ctx context.Context, tenantID, userID string) error
	CreateSession(ctx context.Context, userID, tenantID string, ttl time.Duration) (string, store.Session, error)
	SessionByToken(ctx context.Context, token string, ttl time.Duration) (store.Session, error)
	SwitchSessionTenant(ctx context.Context, sessionID, userID, tenantID string) error
	DeleteSession(ctx context.Context, id string) error
	CreateInvitation(ctx context.Context, tenantID, email string, role store.Role, invitedBy *string, ttl time.Duration) (store.Invitation, string, error)
	InvitationByToken(ctx context.Context, token string) (store.Invitation, error)
	AcceptInvitation(ctx context.Context, token, userID string) (store.Invitation, error)
	ListInvitations(ctx context.Context, tenantID string) ([]store.Invitation, error)
	RevokeInvitation(ctx context.Context, tenantID, id string) error
}

// Server serves the PathLedger API.
type Server struct {
	store Store
	log   *slog.Logger
	now   func() time.Time
	// allowHTTPWebhooks permits plain-http webhook URLs (development only).
	allowHTTPWebhooks bool
	trustProxy        bool

	agentLimit, mgmtLimit, enrollLimit, loginLimit *limiter

	mailer    Mailer
	publicURL string

	agentTokenMaxAge time.Duration
	releases         *releases.Source

	httpMetrics     *metrics.Vec
	ingestedResults *metrics.Counter
	rejectedResults *metrics.Counter
}

// WithAgentTokenMaxAge sets how old an agent credential may get before the
// agent is asked to rotate it (default 30 days).
func WithAgentTokenMaxAge(d time.Duration) Option {
	return func(s *Server) { s.agentTokenMaxAge = d }
}

// WithReleases publishes signed agent releases from src.
func WithReleases(src *releases.Source) Option {
	return func(s *Server) { s.releases = src }
}

// WithMetrics records request and ingest metrics in reg.
func WithMetrics(reg *metrics.Registry) Option {
	return func(s *Server) {
		s.httpMetrics = reg.CounterVec("pathledger_http_request_duration_seconds", "HTTP requests by route and status class.", true, "route", "code")
		s.ingestedResults = reg.Counter("pathledger_results_ingested_total", "Probe results stored.")
		s.rejectedResults = reg.Counter("pathledger_results_rejected_total", "Probe results rejected at ingest.")
	}
}

// TrustProxyHeaders makes rate limits key on X-Forwarded-For; enable only
// when every request arrives through a reverse proxy you control.
func TrustProxyHeaders() Option { return func(s *Server) { s.trustProxy = true } }

// WithLimits overrides the default rate limits (used by tests).
func WithLimits(agent, management, enroll Limit) Option {
	return func(s *Server) {
		s.agentLimit = newLimiter(agent, s.now)
		s.mgmtLimit = newLimiter(management, s.now)
		s.enrollLimit = newLimiter(enroll, s.now)
		s.loginLimit = newLimiter(enroll, s.now)
	}
}

// Option configures a Server.
type Option func(*Server)

// AllowHTTPWebhooks permits http:// webhook URLs; for development only.
func AllowHTTPWebhooks() Option { return func(s *Server) { s.allowHTTPWebhooks = true } }

// New returns a Server.
func New(st Store, log *slog.Logger, opts ...Option) *Server {
	s := &Server{store: st, log: log, now: time.Now, agentTokenMaxAge: 30 * 24 * time.Hour}
	s.agentLimit = newLimiter(limitAgent, s.now)
	s.mgmtLimit = newLimiter(limitManagement, s.now)
	s.enrollLimit = newLimiter(limitEnroll, s.now)
	s.loginLimit = newLimiter(limitLogin, s.now)
	for _, o := range opts {
		o(s)
	}
	return s
}

// Handler returns the root handler with all middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)

	// Agent API.
	mux.HandleFunc("POST /api/v1/agent/enroll", s.enroll)
	mux.Handle("GET /api/v1/agent/plan", s.agentAuth(s.plan))
	mux.Handle("POST /api/v1/agent/results", s.agentAuth(s.ingest))
	mux.Handle("POST /api/v1/agent/credential", s.agentAuth(s.rotateCredential))
	mux.Handle("GET /api/v1/agent/update/manifest.json", s.agentAuth(s.updateManifest))
	mux.Handle("GET /api/v1/agent/update/manifest.json.sig", s.agentAuth(s.updateSignature))
	mux.Handle("GET /api/v1/agent/update/files/{name}", s.agentAuth(s.updateFile))

	// Management API.
	mux.Handle("GET /api/v1/sites", s.read(s.listSites))
	mux.Handle("POST /api/v1/sites", s.write(s.createSite))
	mux.Handle("GET /api/v1/sites/{id}", s.read(s.getSite))
	mux.Handle("POST /api/v1/sites/{id}/enrollment-tokens", s.write(s.createEnrollmentToken))
	mux.Handle("GET /api/v1/agents", s.read(s.listAgents))
	mux.Handle("DELETE /api/v1/agents/{id}", s.write(s.revokeAgent))
	mux.Handle("POST /api/v1/agents/{id}/rotate-credential", s.write(s.requestAgentRotation))
	mux.Handle("GET /api/v1/tests", s.read(s.listTests))
	mux.Handle("POST /api/v1/tests", s.write(s.createTest))
	mux.Handle("PATCH /api/v1/tests/{id}", s.write(s.updateTest))
	mux.Handle("DELETE /api/v1/tests/{id}", s.write(s.deleteTest))
	mux.Handle("GET /api/v1/api-keys", s.write(s.listAPIKeys))
	mux.Handle("POST /api/v1/api-keys", s.write(s.createAPIKey))
	mux.Handle("DELETE /api/v1/api-keys/{id}", s.write(s.revokeAPIKey))
	mux.Handle("GET /api/v1/results", s.read(s.queryResults))
	mux.Handle("GET /api/v1/series", s.read(s.series))
	mux.Handle("GET /api/v1/reports/sla", s.read(s.slaReport))
	mux.Handle("GET /api/v1/events", s.read(s.listEvents))
	mux.Handle("GET /api/v1/events/{id}", s.read(s.getEvent))
	mux.Handle("GET /api/v1/channels", s.read(s.listChannels))
	mux.Handle("POST /api/v1/channels", s.write(s.createChannel))
	mux.Handle("DELETE /api/v1/channels/{id}", s.write(s.deleteChannel))
	mux.Handle("POST /api/v1/channels/{id}/test", s.write(s.testChannel))
	mux.Handle("GET /api/v1/deliveries", s.read(s.listDeliveries))

	// People and sessions.
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.Handle("GET /api/v1/auth/me", s.read(s.me))
	mux.Handle("POST /api/v1/auth/logout", s.sessionOnly(s.logout))
	mux.Handle("POST /api/v1/auth/switch", s.sessionOnly(s.switchTenant))
	mux.Handle("POST /api/v1/auth/password", s.sessionOnly(s.changePassword))
	mux.Handle("GET /api/v1/members", s.read(s.listMembers))
	mux.Handle("PATCH /api/v1/members/{id}", s.write(s.memberRoleChange))
	mux.Handle("DELETE /api/v1/members/{id}", s.write(s.removeMember))
	mux.Handle("GET /api/v1/invitations", s.write(s.listInvitations))
	mux.Handle("POST /api/v1/invitations", s.write(s.createInvitation))
	mux.Handle("DELETE /api/v1/invitations/{id}", s.write(s.revokeInvitation))
	mux.HandleFunc("GET /api/v1/invitations/token/{token}", s.inspectInvitation)
	mux.HandleFunc("POST /api/v1/invitations/token/{token}/accept", s.acceptInvitation)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	// Everything else is the dashboard.
	mux.Handle("/", webui.Handler())

	return s.requestContext(s.recoverPanics(s.accessLog(securityHeaders(mux))))
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		s.log.Error("readiness check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
