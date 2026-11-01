package api

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	if s.rateLimited(w, s.enrollLimit, "ip:"+s.clientIP(r)) {
		return
	}
	var req wire.EnrollRequest
	if !decodeJSON(w, r, r.Body, &req, maxSmallBody) {
		return
	}
	req.Hostname, req.AgentVersion = clip(req.Hostname, 255), clip(req.AgentVersion, 64)
	req.OS, req.Arch = clip(req.OS, 32), clip(req.Arch, 32)
	resp, err := s.store.EnrollAgent(r.Context(), req)
	if errors.Is(err, store.ErrInvalidToken) {
		writeError(w, http.StatusUnauthorized, "invalid_token", "enrolment token is invalid, expired or already used")
		return
	}
	if err != nil {
		s.storeError(w, r, err, "enrolment")
		return
	}
	s.log.InfoContext(r.Context(), "agent enrolled", "agent_id", resp.AgentID, "tenant_id", resp.TenantID, "site_id", resp.SiteID)
	writeJSON(w, http.StatusCreated, resp)
}

// planETag is a strong validator over the plan's content, so agents only
// download and re-apply a plan when it actually changed.
func planETag(tests []wire.Test) (string, error) {
	b, err := json.Marshal(tests)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:16]) + `"`, nil
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	id := agentIdentity(r)
	tests, err := s.store.PlanFor(r.Context(), id.TenantID, id.SiteID)
	if err != nil {
		s.storeError(w, r, err, "plan")
		return
	}
	if tests == nil {
		tests = []wire.Test{}
	}
	etag, err := planETag(tests)
	if err != nil {
		s.storeError(w, r, err, "plan")
		return
	}
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, wire.Plan{Tests: tests, GeneratedAt: s.now().UTC()})
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	var body io.Reader = http.MaxBytesReader(w, r.Body, maxCompressedBody)
	limit := int64(maxCompressedBody)
	switch enc := strings.ToLower(r.Header.Get("Content-Encoding")); enc {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_encoding", "body is not valid gzip")
			return
		}
		defer zr.Close()
		body, limit = zr, maxResultsBody
	default:
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_encoding", "only gzip content encoding is supported")
		return
	}
	var batch wire.ResultBatch
	if !decodeJSON(w, r, body, &batch, limit) {
		return
	}
	if len(batch.Results) > wire.MaxResultsPerBatch {
		writeError(w, http.StatusRequestEntityTooLarge, "batch_too_large", "a batch may hold at most 500 results")
		return
	}
	accepted, rejected, err := s.store.InsertResults(r.Context(), agentIdentity(r), batch.Results)
	if err != nil {
		s.storeError(w, r, err, "results")
		return
	}
	if rejected > 0 {
		s.log.WarnContext(r.Context(), "rejected results from agent", "agent_id", agentIdentity(r).AgentID, "rejected", rejected)
	}
	writeJSON(w, http.StatusOK, wire.IngestResponse{Accepted: accepted, Rejected: rejected})
}
