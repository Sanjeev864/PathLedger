// Package api is the controller's HTTP interface: the agent API used by
// branch agents and the management API used by the dashboard and CLI tools.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/internal/controller/webui"
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
}

// Server serves the PathLedger API.
type Server struct {
	store Store
	log   *slog.Logger
	now   func() time.Time
	// allowHTTPWebhooks permits plain-http webhook URLs (development only).
	allowHTTPWebhooks bool
	trustProxy        bool

	agentLimit, mgmtLimit, enrollLimit *limiter
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
	}
}

// Option configures a Server.
type Option func(*Server)

// AllowHTTPWebhooks permits http:// webhook URLs; for development only.
func AllowHTTPWebhooks() Option { return func(s *Server) { s.allowHTTPWebhooks = true } }

// New returns a Server.
func New(st Store, log *slog.Logger, opts ...Option) *Server {
	s := &Server{store: st, log: log, now: time.Now}
	s.agentLimit = newLimiter(limitAgent, s.now)
	s.mgmtLimit = newLimiter(limitManagement, s.now)
	s.enrollLimit = newLimiter(limitEnroll, s.now)
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

	// Management API.
	mux.Handle("GET /api/v1/sites", s.tenantAuth(s.listSites))
	mux.Handle("POST /api/v1/sites", s.tenantAuth(s.createSite))
	mux.Handle("GET /api/v1/sites/{id}", s.tenantAuth(s.getSite))
	mux.Handle("POST /api/v1/sites/{id}/enrollment-tokens", s.tenantAuth(s.createEnrollmentToken))
	mux.Handle("GET /api/v1/agents", s.tenantAuth(s.listAgents))
	mux.Handle("DELETE /api/v1/agents/{id}", s.tenantAuth(s.revokeAgent))
	mux.Handle("GET /api/v1/tests", s.tenantAuth(s.listTests))
	mux.Handle("POST /api/v1/tests", s.tenantAuth(s.createTest))
	mux.Handle("PATCH /api/v1/tests/{id}", s.tenantAuth(s.updateTest))
	mux.Handle("DELETE /api/v1/tests/{id}", s.tenantAuth(s.deleteTest))
	mux.Handle("GET /api/v1/api-keys", s.tenantAuth(s.listAPIKeys))
	mux.Handle("POST /api/v1/api-keys", s.tenantAuth(s.createAPIKey))
	mux.Handle("DELETE /api/v1/api-keys/{id}", s.tenantAuth(s.revokeAPIKey))
	mux.Handle("GET /api/v1/results", s.tenantAuth(s.queryResults))
	mux.Handle("GET /api/v1/series", s.tenantAuth(s.series))
	mux.Handle("GET /api/v1/events", s.tenantAuth(s.listEvents))
	mux.Handle("GET /api/v1/events/{id}", s.tenantAuth(s.getEvent))
	mux.Handle("GET /api/v1/channels", s.tenantAuth(s.listChannels))
	mux.Handle("POST /api/v1/channels", s.tenantAuth(s.createChannel))
	mux.Handle("DELETE /api/v1/channels/{id}", s.tenantAuth(s.deleteChannel))
	mux.Handle("POST /api/v1/channels/{id}/test", s.tenantAuth(s.testChannel))
	mux.Handle("GET /api/v1/deliveries", s.tenantAuth(s.listDeliveries))

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
