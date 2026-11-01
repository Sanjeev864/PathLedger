package api

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/releases"
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
func planETag(p wire.Plan) (string, error) {
	p.GeneratedAt = time.Time{}
	b, err := json.Marshal(p)
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
	if v, ok := agentVersion(r.UserAgent()); ok {
		if err := s.store.SetAgentVersion(r.Context(), id.AgentID, v); err != nil {
			s.log.WarnContext(r.Context(), "recording agent version failed", "error", err)
		}
	}
	p := wire.Plan{Tests: tests, RotateCredential: s.now().Sub(id.TokenRotatedAt) > s.agentTokenMaxAge}
	if s.releases != nil {
		if rel, err := s.releases.Current(); err != nil {
			s.log.ErrorContext(r.Context(), "agent release directory is unusable; not offering updates", "error", err)
		} else if rel != nil {
			p.Update = &wire.UpdateHint{Version: rel.Manifest.Version}
		}
	}
	etag, err := planETag(p)
	if err != nil {
		s.storeError(w, r, err, "plan")
		return
	}
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	p.GeneratedAt = s.now().UTC()
	writeJSON(w, http.StatusOK, p)
}

// agentVersion extracts the version from "PathLedger-Agent/<version> (...)".
func agentVersion(ua string) (string, bool) {
	rest, ok := strings.CutPrefix(ua, "PathLedger-Agent/")
	if !ok {
		return "", false
	}
	v, _, _ := strings.Cut(rest, " ")
	if v == "" || len(v) > 64 {
		return "", false
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '-' || c == '+' || c == '_') {
			return "", false
		}
	}
	return v, true
}

// rotateCredential issues the calling agent a new credential. The old one
// stays valid until the new one is first used.
func (s *Server) rotateCredential(w http.ResponseWriter, r *http.Request) {
	id := agentIdentity(r)
	token, err := s.store.RotateAgentToken(r.Context(), id.AgentID)
	if err != nil {
		s.storeError(w, r, err, "agent")
		return
	}
	s.log.InfoContext(r.Context(), "agent credential rotated", "agent_id", id.AgentID)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, wire.CredentialResponse{AgentToken: token})
}

func (s *Server) currentRelease(w http.ResponseWriter, r *http.Request) *releases.Release {
	if s.releases == nil {
		writeError(w, http.StatusNotFound, "not_found", "this controller does not distribute agent updates")
		return nil
	}
	rel, err := s.releases.Current()
	if err != nil || rel == nil {
		writeError(w, http.StatusNotFound, "not_found", "no agent release is published")
		return nil
	}
	return rel
}

func (s *Server) updateManifest(w http.ResponseWriter, r *http.Request) {
	if rel := s.currentRelease(w, r); rel != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(rel.Raw)
	}
}

func (s *Server) updateSignature(w http.ResponseWriter, r *http.Request) {
	if rel := s.currentRelease(w, r); rel != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(rel.Signature))
	}
}

func (s *Server) updateFile(w http.ResponseWriter, r *http.Request) {
	if s.releases == nil {
		writeError(w, http.StatusNotFound, "not_found", "this controller does not distribute agent updates")
		return
	}
	f, size, err := s.releases.Open(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such release file")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if _, err := io.CopyN(w, f, size); err != nil {
		s.log.WarnContext(r.Context(), "sending release file failed", "error", err)
	}
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
	if s.ingestedResults != nil {
		s.ingestedResults.Add(uint64(accepted))
		s.rejectedResults.Add(uint64(rejected))
	}
	if rejected > 0 {
		s.log.WarnContext(r.Context(), "rejected results from agent", "agent_id", agentIdentity(r).AgentID, "rejected", rejected)
	}
	writeJSON(w, http.StatusOK, wire.IngestResponse{Accepted: accepted, Rejected: rejected})
}
