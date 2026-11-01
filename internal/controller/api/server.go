// Package api is the controller's HTTP interface: the agent API used by
// branch agents and the management API used by the dashboard and CLI tools.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/store"
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
}

// Server serves the PathLedger API.
type Server struct {
	store Store
	log   *slog.Logger
	now   func() time.Time
}

// New returns a Server.
func New(st Store, log *slog.Logger) *Server {
	return &Server{store: st, log: log, now: time.Now}
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
	mux.Handle("DELETE /api/v1/tests/{id}", s.tenantAuth(s.deleteTest))
	mux.Handle("GET /api/v1/results", s.tenantAuth(s.queryResults))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})

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
