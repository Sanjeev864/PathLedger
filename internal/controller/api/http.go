package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/auth"
	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

// Request body limits.
const (
	maxSmallBody      = 16 << 10
	maxCompressedBody = 2 << 20
	maxResultsBody    = 8 << 20
)

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyTenant
	keyAgent
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validID(id string) bool { return uuidRE.MatchString(id) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, wire.ErrorBody{Error: wire.ErrorDetail{Code: code, Message: message}})
}

// decodeJSON strictly decodes a single JSON object from r into dst.
func decodeJSON(w http.ResponseWriter, r *http.Request, body io.Reader, dst any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, io.NopCloser(body), limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("request body exceeds %d bytes", limit))
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON for this endpoint: "+err.Error())
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain a single JSON object")
		return false
	}
	return true
}

// storeError maps store errors onto HTTP responses. Unexpected errors are
// logged with the request ID and returned as an opaque 500.
func (s *Server) storeError(w http.ResponseWriter, r *http.Request, err error, what string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+" not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", what+" already exists")
	case wire.IsValidationError(err):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, context.Canceled):
		// Client went away; nothing useful to send.
	default:
		s.log.ErrorContext(r.Context(), "request failed", "error", err, "request_id", requestID(r))
		writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred; request ID "+requestID(r))
	}
}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(keyRequestID).(string)
	return id
}

func tenantID(r *http.Request) string {
	id, _ := r.Context().Value(keyTenant).(string)
	return id
}

func agentIdentity(r *http.Request) store.AgentIdentity {
	id, _ := r.Context().Value(keyAgent).(store.AgentIdentity)
	return id
}

// requestContext assigns each request an ID, echoed in X-Request-ID.
func (s *Server) requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		id := hex.EncodeToString(b)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyRequestID, id)))
	})
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic serving request", "panic", v, "request_id", requestID(r), "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// accessLog records one line per request. It never logs headers, so
// credentials cannot leak into logs.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			level = slog.LevelDebug
		}
		if rec.status >= 500 {
			level = slog.LevelError
		}
		s.log.Log(r.Context(), level, "http request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(), "request_id", requestID(r))
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// tenantAuth requires a valid management API key.
func (s *Server) tenantAuth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := auth.BearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "an API key is required (Authorization: Bearer plk_...)")
			return
		}
		tenant, _, err := s.store.AuthenticateAPIKey(r.Context(), tok)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or revoked API key")
			return
		}
		if err != nil {
			s.storeError(w, r, err, "api key")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), keyTenant, tenant)))
	})
}

// agentAuth requires a valid, unrevoked agent credential.
func (s *Server) agentAuth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := auth.BearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "an agent token is required")
			return
		}
		id, err := s.store.AuthenticateAgent(r.Context(), tok)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or revoked agent token")
			return
		}
		if err != nil {
			s.storeError(w, r, err, "agent")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), keyAgent, id)))
	})
}
