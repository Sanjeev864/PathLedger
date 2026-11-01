package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/auth"
	"github.com/sanjeev864/pathledger/internal/controller/store"
)

const (
	sessionCookie = "pathledger_session"
	csrfHeader    = "X-CSRF-Token"
	sessionTTL    = 24 * time.Hour // slides forward while in use
	inviteTTL     = 7 * 24 * time.Hour
)

// Mailer sends invitation emails. It is optional; without it the inviter
// shares the link themselves.
type Mailer interface {
	SendText(ctx context.Context, to []string, subject, body string) error
}

// WithMailer enables invitation emails.
func WithMailer(m Mailer) Option { return func(s *Server) { s.mailer = m } }

// WithPublicURL sets the dashboard URL used in invitation links.
func WithPublicURL(u string) Option {
	return func(s *Server) { s.publicURL = strings.TrimRight(u, "/") }
}

// principal is whoever is making a management request.
type principal struct {
	TenantID  string
	Role      store.Role
	UserID    string // empty for API keys
	APIKeyID  string // empty for sessions
	SessionID string
	CSRF      string
	Email     string
	Name      string
}

const keyPrincipal ctxKey = 100

func principalOf(r *http.Request) principal {
	p, _ := r.Context().Value(keyPrincipal).(principal)
	return p
}

func safeMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }

// authenticate resolves an API key (Authorization: Bearer plk_…) or a
// session cookie. Session requests that change state must carry the
// session's CSRF token in X-CSRF-Token.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (principal, bool) {
	if tok, ok := auth.BearerToken(r); ok {
		tenant, keyID, err := s.store.AuthenticateAPIKey(r.Context(), tok)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or revoked API key")
			return principal{}, false
		}
		if err != nil {
			s.storeError(w, r, err, "api key")
			return principal{}, false
		}
		// API keys act with admin rights within their tenant.
		return principal{TenantID: tenant, Role: store.RoleAdmin, APIKeyID: keyID}, true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "sign in, or send an API key (Authorization: Bearer plk_...)")
		return principal{}, false
	}
	sess, err := s.store.SessionByToken(r.Context(), c.Value, sessionTTL)
	if errors.Is(err, store.ErrNotFound) {
		clearSessionCookie(w)
		writeError(w, http.StatusUnauthorized, "unauthorized", "your session has expired; sign in again")
		return principal{}, false
	}
	if err != nil {
		s.storeError(w, r, err, "session")
		return principal{}, false
	}
	if !safeMethod(r.Method) && subtle.ConstantTimeCompare([]byte(r.Header.Get(csrfHeader)), []byte(sess.CSRF)) != 1 {
		writeError(w, http.StatusForbidden, "csrf_failed", "missing or invalid "+csrfHeader+" header")
		return principal{}, false
	}
	return principal{TenantID: sess.TenantID, Role: sess.Role, UserID: sess.UserID, SessionID: sess.ID,
		CSRF: sess.CSRF, Email: sess.Email, Name: sess.Name}, true
}

// require wraps a handler with authentication, a minimum role and the
// management rate limit.
func (s *Server) require(min store.Role, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticate(w, r)
		if !ok {
			return
		}
		if !p.Role.AtLeast(min) {
			writeError(w, http.StatusForbidden, "forbidden", "your role ("+string(p.Role)+") does not allow this; "+string(min)+" required")
			return
		}
		if s.rateLimited(w, s.mgmtLimit, "tenant:"+p.TenantID) {
			return
		}
		ctx := context.WithValue(r.Context(), keyPrincipal, p)
		ctx = context.WithValue(ctx, keyTenant, p.TenantID)
		ctx = context.WithValue(ctx, keyAPIKey, p.APIKeyID)
		h(w, r.WithContext(ctx))
	})
}

func (s *Server) read(h http.HandlerFunc) http.Handler  { return s.require(store.RoleViewer, h) }
func (s *Server) write(h http.HandlerFunc) http.Handler { return s.require(store.RoleAdmin, h) }

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int((30 * 24 * time.Hour).Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

type meResponse struct {
	Auth        string             `json:"auth"` // "session" or "api_key"
	User        *meUser            `json:"user"`
	TenantID    string             `json:"tenant_id"`
	Role        store.Role         `json:"role"`
	CSRFToken   string             `json:"csrf_token,omitempty"`
	Memberships []store.Membership `json:"memberships"`
}

type meUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

func (s *Server) meFor(ctx context.Context, p principal) (meResponse, error) {
	if p.UserID == "" {
		return meResponse{Auth: "api_key", TenantID: p.TenantID, Role: p.Role, Memberships: []store.Membership{}}, nil
	}
	ms, err := s.store.Memberships(ctx, p.UserID)
	if err != nil {
		return meResponse{}, err
	}
	return meResponse{Auth: "session", User: &meUser{ID: p.UserID, Email: p.Email, Name: p.Name},
		TenantID: p.TenantID, Role: p.Role, CSRFToken: p.CSRF, Memberships: ms}, nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	resp, err := s.meFor(r.Context(), principalOf(r))
	if err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TenantID string `json:"tenant_id"`
}

func normEmail(e string) (string, bool) {
	a, err := mail.ParseAddress(strings.TrimSpace(e))
	if err != nil || len(a.Address) > 254 {
		return "", false
	}
	return strings.ToLower(a.Address), true
}

var errBadLogin = "incorrect email or password"

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.rateLimited(w, s.loginLimit, "ip:"+s.clientIP(r)) {
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, r.Body, &req, maxSmallBody) {
		return
	}
	email, ok := normEmail(req.Email)
	if !ok || len(req.Password) > auth.MaxPasswordLen {
		auth.DummyVerify(req.Password)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", errBadLogin)
		return
	}
	if s.rateLimited(w, s.loginLimit, "email:"+email) {
		return
	}
	u, err := s.store.UserByEmail(r.Context(), email)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.DisabledAt != nil) {
		auth.DummyVerify(req.Password)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", errBadLogin)
		return
	}
	if err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	if auth.VerifyPassword(req.Password, u.PasswordHash) != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", errBadLogin)
		return
	}
	s.startSession(w, r, u, req.TenantID)
}

// startSession signs u in to tenantID, or to their first tenant.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User, tenantID string) {
	ms, err := s.store.Memberships(r.Context(), u.ID)
	if err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	if len(ms) == 0 {
		writeError(w, http.StatusForbidden, "no_tenant", "this account does not belong to any organisation yet")
		return
	}
	if tenantID == "" {
		tenantID = ms[0].TenantID
	}
	token, sess, err := s.store.CreateSession(r.Context(), u.ID, tenantID, sessionTTL)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusForbidden, "forbidden", "you are not a member of that organisation")
		return
	}
	if err != nil {
		s.storeError(w, r, err, "session")
		return
	}
	_ = s.store.RecordLogin(r.Context(), u.ID)
	setSessionCookie(w, token)
	var role store.Role
	for _, m := range ms {
		if m.TenantID == tenantID {
			role = m.Role
		}
	}
	s.log.InfoContext(r.Context(), "user signed in", "user_id", u.ID, "tenant_id", tenantID)
	writeJSON(w, http.StatusOK, meResponse{Auth: "session", User: &meUser{ID: u.ID, Email: u.Email, Name: u.Name},
		TenantID: tenantID, Role: role, CSRFToken: sess.CSRF, Memberships: ms})
}

// sessionOnly wraps endpoints that make sense only for signed-in users.
func (s *Server) sessionOnly(h http.HandlerFunc) http.Handler {
	return s.require(store.RoleViewer, func(w http.ResponseWriter, r *http.Request) {
		if principalOf(r).UserID == "" {
			writeError(w, http.StatusBadRequest, "session_required", "this endpoint needs a signed-in user, not an API key")
			return
		}
		h(w, r)
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_ = s.store.DeleteSession(r.Context(), principalOf(r).SessionID)
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) switchTenant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantID string `json:"tenant_id"`
	}
	if !decodeJSON(w, r, r.Body, &req, maxSmallBody) {
		return
	}
	p := principalOf(r)
	if !validID(req.TenantID) {
		writeError(w, http.StatusNotFound, "not_found", "organisation not found")
		return
	}
	if err := s.store.SwitchSessionTenant(r.Context(), p.SessionID, p.UserID, req.TenantID); err != nil {
		s.storeError(w, r, err, "organisation")
		return
	}
	role, _ := s.store.MemberRole(r.Context(), req.TenantID, p.UserID)
	p.TenantID, p.Role = req.TenantID, role
	resp, err := s.meFor(r.Context(), p)
	if err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decodeJSON(w, r, r.Body, &req, maxSmallBody) {
		return
	}
	p := principalOf(r)
	if s.rateLimited(w, s.loginLimit, "user:"+p.UserID) {
		return
	}
	u, err := s.store.UserByID(r.Context(), p.UserID)
	if err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	if len(req.Current) > auth.MaxPasswordLen || auth.VerifyPassword(req.Current, u.PasswordHash) != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
		return
	}
	if err := auth.CheckPasswordPolicy(req.New); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "new_password: "+err.Error())
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		s.storeError(w, r, err, "password")
		return
	}
	if err := s.store.SetPassword(r.Context(), p.UserID, hash); err != nil {
		s.storeError(w, r, err, "password")
		return
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "password changed; sign in again"})
}
