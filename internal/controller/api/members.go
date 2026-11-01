package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sanjeev864/pathledger/internal/controller/auth"
	"github.com/sanjeev864/pathledger/internal/controller/store"
)

// canAssign enforces that only owners create, promote to, or demote from
// the owner role.
func canAssign(actor, from, to store.Role) bool {
	if from == store.RoleOwner || to == store.RoleOwner {
		return actor == store.RoleOwner
	}
	return actor.AtLeast(store.RoleAdmin)
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	ms, err := s.store.Members(r.Context(), tenantID(r))
	if err != nil {
		s.storeError(w, r, err, "members")
		return
	}
	writeJSON(w, http.StatusOK, newList(ms))
}

func (s *Server) memberRoleChange(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r, "member")
	if !ok {
		return
	}
	var req struct {
		Role store.Role `json:"role"`
	}
	if !decodeJSON(w, r, r.Body, &req, maxSmallBody) {
		return
	}
	if !req.Role.Valid() {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "role: owner, admin or viewer")
		return
	}
	current, err := s.store.MemberRole(r.Context(), tenantID(r), userID)
	if err != nil {
		s.storeError(w, r, err, "member")
		return
	}
	if !canAssign(principalOf(r).Role, current, req.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "only owners can grant or remove the owner role")
		return
	}
	if err := s.store.SetMemberRole(r.Context(), tenantID(r), userID, req.Role); err != nil {
		s.memberError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user_id": userID, "role": string(req.Role)})
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r, "member")
	if !ok {
		return
	}
	current, err := s.store.MemberRole(r.Context(), tenantID(r), userID)
	if err != nil {
		s.storeError(w, r, err, "member")
		return
	}
	if current == store.RoleOwner && principalOf(r).Role != store.RoleOwner {
		writeError(w, http.StatusForbidden, "forbidden", "only owners can remove an owner")
		return
	}
	if err := s.store.RemoveMember(r.Context(), tenantID(r), userID); err != nil {
		s.memberError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) memberError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrLastOwner) {
		writeError(w, http.StatusConflict, "last_owner", "an organisation must keep at least one owner")
		return
	}
	s.storeError(w, r, err, "member")
}

type inviteResponse struct {
	store.Invitation
	Token string `json:"token"`
	Link  string `json:"link"`
	Sent  bool   `json:"emailed"`
}

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string     `json:"email"`
		Role  store.Role `json:"role"`
	}
	if !decodeJSON(w, r, r.Body, &req, maxSmallBody) {
		return
	}
	email, ok := normEmail(req.Email)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "email: not a valid address")
		return
	}
	if !req.Role.Valid() {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "role: owner, admin or viewer")
		return
	}
	p := principalOf(r)
	if !canAssign(p.Role, store.RoleViewer, req.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "only owners can invite owners")
		return
	}
	var by *string
	if p.UserID != "" {
		by = &p.UserID
	}
	inv, token, err := s.store.CreateInvitation(r.Context(), p.TenantID, email, req.Role, by, inviteTTL)
	if err != nil {
		s.storeError(w, r, err, "invitation")
		return
	}
	resp := inviteResponse{Invitation: inv, Token: token, Link: s.publicURL + "/#/invite/" + token}
	if s.mailer != nil && s.publicURL != "" {
		body := fmt.Sprintf("You have been invited to PathLedger as %s.\n\nAccept the invitation (valid for 7 days):\n%s\n\nIf you were not expecting this, ignore this email.\n", req.Role, resp.Link)
		if err := s.mailer.SendText(r.Context(), []string{email}, "You're invited to PathLedger", body); err != nil {
			s.log.WarnContext(r.Context(), "invitation email failed; share the link manually", "error", err)
		} else {
			resp.Sent = true
		}
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) listInvitations(w http.ResponseWriter, r *http.Request) {
	invs, err := s.store.ListInvitations(r.Context(), tenantID(r))
	if err != nil {
		s.storeError(w, r, err, "invitations")
		return
	}
	writeJSON(w, http.StatusOK, newList(invs))
}

func (s *Server) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "invitation")
	if !ok {
		return
	}
	if err := s.store.RevokeInvitation(r.Context(), tenantID(r), id); err != nil {
		s.storeError(w, r, err, "invitation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// inspectInvitation lets the invite page show what is being accepted. It is
// public but reveals nothing without the 256-bit token.
func (s *Server) inspectInvitation(w http.ResponseWriter, r *http.Request) {
	if s.rateLimited(w, s.loginLimit, "ip:"+s.clientIP(r)) {
		return
	}
	inv, err := s.store.InvitationByToken(r.Context(), r.PathValue("token"))
	if errors.Is(err, store.ErrInvalidToken) {
		writeError(w, http.StatusNotFound, "invalid_invitation", "this invitation is invalid, expired or already used")
		return
	}
	if err != nil {
		s.storeError(w, r, err, "invitation")
		return
	}
	_, uerr := s.store.UserByEmail(r.Context(), inv.Email)
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_name": inv.TenantName, "email": inv.Email, "role": inv.Role, "expires_at": inv.ExpiresAt,
		"existing_user": uerr == nil,
	})
}

// acceptInvitation signs in an existing user (password check) or creates a
// new account, joins the tenant, and starts a session there.
func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	if s.rateLimited(w, s.loginLimit, "ip:"+s.clientIP(r)) {
		return
	}
	token := r.PathValue("token")
	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, r.Body, &req, maxSmallBody) {
		return
	}
	inv, err := s.store.InvitationByToken(r.Context(), token)
	if errors.Is(err, store.ErrInvalidToken) {
		writeError(w, http.StatusNotFound, "invalid_invitation", "this invitation is invalid, expired or already used")
		return
	}
	if err != nil {
		s.storeError(w, r, err, "invitation")
		return
	}
	u, err := s.store.UserByEmail(r.Context(), inv.Email)
	switch {
	case err == nil:
		if u.DisabledAt != nil || len(req.Password) > auth.MaxPasswordLen || auth.VerifyPassword(req.Password, u.PasswordHash) != nil {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "enter the password of your existing PathLedger account")
			return
		}
	case errors.Is(err, store.ErrNotFound):
		if perr := auth.CheckPasswordPolicy(req.Password); perr != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "password: "+perr.Error())
			return
		}
		name := strings.TrimSpace(req.Name)
		if len(name) > 200 {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "name: at most 200 characters")
			return
		}
		hash, herr := auth.HashPassword(req.Password)
		if herr != nil {
			s.storeError(w, r, herr, "user")
			return
		}
		if u, err = s.store.CreateUser(r.Context(), inv.Email, name, hash); err != nil {
			s.storeError(w, r, err, "user")
			return
		}
	default:
		s.storeError(w, r, err, "user")
		return
	}
	if _, err := s.store.AcceptInvitation(r.Context(), token, u.ID); err != nil {
		if errors.Is(err, store.ErrInvalidToken) {
			writeError(w, http.StatusNotFound, "invalid_invitation", "this invitation is invalid, expired or already used")
			return
		}
		s.storeError(w, r, err, "invitation")
		return
	}
	s.startSession(w, r, u, inv.TenantID)
}
