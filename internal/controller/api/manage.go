package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

// Enrolment token lifetime bounds.
const (
	defaultEnrollTTL = 24 * time.Hour
	maxEnrollTTL     = 7 * 24 * time.Hour
)

// Results query bounds.
const (
	defaultResultsWindow = time.Hour
	maxResultsWindow     = 31 * 24 * time.Hour
	defaultResultsLimit  = 1000
	maxResultsLimit      = 10_000
)

// list wraps collections so the response can gain paging fields later
// without breaking clients.
type list[T any] struct {
	Items []T `json:"items"`
}

func newList[T any](items []T) list[T] {
	if items == nil {
		items = []T{}
	}
	return list[T]{Items: items}
}

func pathID(w http.ResponseWriter, r *http.Request, what string) (string, bool) {
	id := r.PathValue("id")
	if !validID(id) {
		writeError(w, http.StatusNotFound, "not_found", what+" not found")
		return "", false
	}
	return id, true
}

type siteRequest struct {
	Name     string `json:"name"`
	Location string `json:"location"`
}

func (s *Server) createSite(w http.ResponseWriter, r *http.Request) {
	var req siteRequest
	if !decodeJSON(w, r, r.Body, &req, maxSmallBody) {
		return
	}
	req.Name, req.Location = strings.TrimSpace(req.Name), strings.TrimSpace(req.Location)
	if req.Name == "" || len(req.Name) > 200 {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "name: must be 1 to 200 characters")
		return
	}
	if len(req.Location) > 500 {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "location: must be at most 500 characters")
		return
	}
	site, err := s.store.CreateSite(r.Context(), tenantID(r), req.Name, req.Location)
	if err != nil {
		s.storeError(w, r, err, "site")
		return
	}
	writeJSON(w, http.StatusCreated, site)
}

func (s *Server) listSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.store.ListSites(r.Context(), tenantID(r))
	if err != nil {
		s.storeError(w, r, err, "sites")
		return
	}
	writeJSON(w, http.StatusOK, newList(sites))
}

func (s *Server) getSite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "site")
	if !ok {
		return
	}
	site, err := s.store.GetSite(r.Context(), tenantID(r), id)
	if err != nil {
		s.storeError(w, r, err, "site")
		return
	}
	writeJSON(w, http.StatusOK, site)
}

type enrollmentTokenRequest struct {
	TTLHours int `json:"ttl_hours"`
}

type enrollmentTokenResponse struct {
	store.EnrollmentToken
	Token string `json:"token"`
}

func (s *Server) createEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	siteID, ok := pathID(w, r, "site")
	if !ok {
		return
	}
	var req enrollmentTokenRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, r.Body, &req, maxSmallBody) {
		return
	}
	ttl := defaultEnrollTTL
	if req.TTLHours != 0 {
		ttl = time.Duration(req.TTLHours) * time.Hour
		if req.TTLHours < 1 || ttl > maxEnrollTTL {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "ttl_hours: must be between 1 and 168")
			return
		}
	}
	et, token, err := s.store.CreateEnrollmentToken(r.Context(), tenantID(r), siteID, ttl)
	if err != nil {
		s.storeError(w, r, err, "site")
		return
	}
	writeJSON(w, http.StatusCreated, enrollmentTokenResponse{EnrollmentToken: et, Token: token})
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgents(r.Context(), tenantID(r))
	if err != nil {
		s.storeError(w, r, err, "agents")
		return
	}
	writeJSON(w, http.StatusOK, newList(agents))
}

func (s *Server) revokeAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "agent")
	if !ok {
		return
	}
	if err := s.store.RevokeAgent(r.Context(), tenantID(r), id); err != nil {
		s.storeError(w, r, err, "agent")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type testRequest struct {
	SiteID          *string         `json:"site_id"`
	Kind            wire.ProbeKind  `json:"kind"`
	Target          string          `json:"target"`
	IntervalSeconds int             `json:"interval_seconds"`
	TimeoutMillis   int             `json:"timeout_millis"`
	Params          wire.TestParams `json:"params"`
}

func (s *Server) createTest(w http.ResponseWriter, r *http.Request) {
	var req testRequest
	if !decodeJSON(w, r, r.Body, &req, maxSmallBody) {
		return
	}
	if req.SiteID != nil && !validID(*req.SiteID) {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "site_id: must be a site ID or null for all sites")
		return
	}
	t := wire.Test{
		Kind: req.Kind, Target: strings.TrimSpace(req.Target),
		IntervalSeconds: req.IntervalSeconds, TimeoutMillis: req.TimeoutMillis, Params: req.Params,
	}
	if err := t.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
		return
	}
	created, err := s.store.CreateTest(r.Context(), tenantID(r), req.SiteID, t)
	if err != nil {
		s.storeError(w, r, err, "site")
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listTests(w http.ResponseWriter, r *http.Request) {
	tests, err := s.store.ListTests(r.Context(), tenantID(r))
	if err != nil {
		s.storeError(w, r, err, "tests")
		return
	}
	writeJSON(w, http.StatusOK, newList(tests))
}

func (s *Server) deleteTest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "test")
	if !ok {
		return
	}
	if err := s.store.DeleteTest(r.Context(), tenantID(r), id); err != nil {
		s.storeError(w, r, err, "test")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) queryResults(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ResultFilter{SiteID: q.Get("site_id"), TestID: q.Get("test_id"), Limit: defaultResultsLimit}
	for name, v := range map[string]string{"site_id": f.SiteID, "test_id": f.TestID} {
		if v != "" && !validID(v) {
			writeError(w, http.StatusBadRequest, "invalid_query", name+": must be an ID")
			return
		}
	}
	f.To = s.now().UTC()
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query", "to: must be an RFC 3339 timestamp")
			return
		}
		f.To = t
	}
	f.From = f.To.Add(-defaultResultsWindow)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query", "from: must be an RFC 3339 timestamp")
			return
		}
		f.From = t
	}
	if !f.From.Before(f.To) || f.To.Sub(f.From) > maxResultsWindow {
		writeError(w, http.StatusBadRequest, "invalid_query", "from must be before to, and the window at most 31 days")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxResultsLimit {
			writeError(w, http.StatusBadRequest, "invalid_query", "limit: must be between 1 and 10000")
			return
		}
		f.Limit = n
	}
	results, err := s.store.QueryResults(r.Context(), tenantID(r), f)
	if err != nil {
		s.storeError(w, r, err, "results")
		return
	}
	writeJSON(w, http.StatusOK, newList(results))
}
