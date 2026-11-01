package api

import (
	"net/http"
	"strings"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// testPatch holds the fields a PATCH may change; nil means "leave as is".
type testPatch struct {
	Target          *string          `json:"target"`
	IntervalSeconds *int             `json:"interval_seconds"`
	TimeoutMillis   *int             `json:"timeout_millis"`
	Params          *wire.TestParams `json:"params"`
	Enabled         *bool            `json:"enabled"`
}

func (s *Server) updateTest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "test")
	if !ok {
		return
	}
	var p testPatch
	if !decodeJSON(w, r, r.Body, &p, maxSmallBody) {
		return
	}
	t, err := s.store.GetTest(r.Context(), tenantID(r), id)
	if err != nil {
		s.storeError(w, r, err, "test")
		return
	}
	if p.Target != nil {
		t.Target = strings.TrimSpace(*p.Target)
	}
	if p.IntervalSeconds != nil {
		t.IntervalSeconds = *p.IntervalSeconds
	}
	if p.TimeoutMillis != nil {
		t.TimeoutMillis = *p.TimeoutMillis
	}
	if p.Params != nil {
		t.Params = *p.Params
	}
	if p.Enabled != nil {
		t.Enabled = *p.Enabled
	}
	updated, err := s.store.UpdateTest(r.Context(), tenantID(r), t)
	if err != nil {
		s.storeError(w, r, err, "test")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

type apiKeyRequest struct {
	Name string `json:"name"`
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var req apiKeyRequest
	if !decodeJSON(w, r, r.Body, &req, maxSmallBody) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 200 {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "name: must be 1 to 200 characters")
		return
	}
	k, token, err := s.store.CreateAPIKey(r.Context(), tenantID(r), req.Name)
	if err != nil {
		s.storeError(w, r, err, "api key")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": k, "token": token})
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListAPIKeys(r.Context(), tenantID(r))
	if err != nil {
		s.storeError(w, r, err, "api keys")
		return
	}
	writeJSON(w, http.StatusOK, newList(keys))
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "api key")
	if !ok {
		return
	}
	if id == apiKeyID(r) {
		writeError(w, http.StatusConflict, "conflict", "cannot revoke the key used for this request; use another key")
		return
	}
	if err := s.store.RevokeAPIKey(r.Context(), tenantID(r), id); err != nil {
		s.storeError(w, r, err, "api key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
