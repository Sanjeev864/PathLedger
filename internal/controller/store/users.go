package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sanjeev864/pathledger/internal/controller/auth"
)

// Role is a user's permission level within a tenant.
type Role string

// Roles, from most to least privileged.
const (
	RoleOwner  Role = "owner"  // everything, including managing owners
	RoleAdmin  Role = "admin"  // configure sites, tests, channels, keys, members
	RoleViewer Role = "viewer" // read-only
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleOwner || r == RoleAdmin || r == RoleViewer }

// AtLeast reports whether r grants at least the privileges of min.
func (r Role) AtLeast(min Role) bool { return rank(r) >= rank(min) }

func rank(r Role) int {
	switch r {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// ErrLastOwner protects tenants from losing their final owner.
var ErrLastOwner = errors.New("a tenant must keep at least one owner")

// User is a person who signs in to the dashboard.
type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	DisabledAt   *time.Time `json:"disabled_at"`
	PasswordHash string     `json:"-"`
}

// Membership is a user's role in one tenant.
type Membership struct {
	TenantID   string `json:"tenant_id"`
	TenantName string `json:"tenant_name"`
	Role       Role   `json:"role"`
}

// Member is a user as seen from a tenant.
type Member struct {
	UserID      string     `json:"user_id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        Role       `json:"role"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

// Session is an authenticated browser session, scoped to one tenant at a time.
type Session struct {
	ID        string
	UserID    string
	Email     string
	Name      string
	TenantID  string
	Role      Role
	CSRF      string
	ExpiresAt time.Time
}

// CreateUser stores a new user. hash must come from auth.HashPassword.
func (s *Store) CreateUser(ctx context.Context, email, name, hash string) (User, error) {
	u := User{Email: email, Name: name}
	err := s.pool.QueryRow(ctx,
		"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id, created_at",
		email, name, hash).Scan(&u.ID, &u.CreatedAt)
	return u, mapErr(err)
}

// UserByEmail returns a user, including the password hash, for sign-in.
func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `SELECT id, email, name, created_at, last_login_at, disabled_at, password_hash
		FROM users WHERE email = $1`, email).
		Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt, &u.LastLoginAt, &u.DisabledAt, &u.PasswordHash)
	return u, mapErr(err)
}

// UserByID returns a user, including the password hash.
func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `SELECT id, email, name, created_at, last_login_at, disabled_at, password_hash
		FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt, &u.LastLoginAt, &u.DisabledAt, &u.PasswordHash)
	return u, mapErr(err)
}

// RecordLogin stamps a successful sign-in.
func (s *Store) RecordLogin(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, "UPDATE users SET last_login_at = now() WHERE id = $1", userID)
	return err
}

// SetPassword replaces a user's password hash and ends all their sessions.
func (s *Store) SetPassword(ctx context.Context, userID, hash string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE users SET password_hash = $2 WHERE id = $1", userID, hash); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1", userID)
		return err
	})
}

// AddMembership grants a user a role in a tenant (or changes it).
func (s *Store) AddMembership(ctx context.Context, userID, tenantID string, role Role) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO memberships (user_id, tenant_id, role) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, tenant_id) DO UPDATE SET role = EXCLUDED.role`, userID, tenantID, role)
	return mapErr(err)
}

// Memberships lists the tenants a user belongs to.
func (s *Store) Memberships(ctx context.Context, userID string) ([]Membership, error) {
	rows, err := s.pool.Query(ctx, `SELECT m.tenant_id, t.name, m.role FROM memberships m
		JOIN tenants t ON t.id = m.tenant_id WHERE m.user_id = $1 ORDER BY t.name`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Membership, error) {
		var m Membership
		err := r.Scan(&m.TenantID, &m.TenantName, &m.Role)
		return m, err
	})
}

// Members lists a tenant's users.
func (s *Store) Members(ctx context.Context, tenantID string) ([]Member, error) {
	rows, err := s.pool.Query(ctx, `SELECT u.id, u.email, u.name, m.role, u.last_login_at FROM memberships m
		JOIN users u ON u.id = m.user_id WHERE m.tenant_id = $1 ORDER BY u.email`, tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Member, error) {
		var m Member
		err := r.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.LastLoginAt)
		return m, err
	})
}

// MemberRole returns a user's role in a tenant.
func (s *Store) MemberRole(ctx context.Context, tenantID, userID string) (Role, error) {
	var r Role
	err := s.pool.QueryRow(ctx, "SELECT role FROM memberships WHERE tenant_id = $1 AND user_id = $2", tenantID, userID).Scan(&r)
	return r, mapErr(err)
}

// guardLastOwner fails if removing or demoting userID would leave the
// tenant with no owner. It locks the tenant's owner rows first.
func guardLastOwner(ctx context.Context, tx pgx.Tx, tenantID, userID string) error {
	rows, err := tx.Query(ctx, "SELECT user_id FROM memberships WHERE tenant_id = $1 AND role = 'owner' FOR UPDATE", tenantID)
	if err != nil {
		return err
	}
	owners, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if len(owners) == 1 && owners[0] == userID {
		return ErrLastOwner
	}
	return nil
}

// SetMemberRole changes a member's role.
func (s *Store) SetMemberRole(ctx context.Context, tenantID, userID string, role Role) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if role != RoleOwner {
			if err := guardLastOwner(ctx, tx, tenantID, userID); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, "UPDATE memberships SET role = $3 WHERE tenant_id = $1 AND user_id = $2", tenantID, userID, role)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return err
	})
}

// RemoveMember removes a user from a tenant and ends their sessions there.
func (s *Store) RemoveMember(ctx context.Context, tenantID, userID string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := guardLastOwner(ctx, tx, tenantID, userID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2", tenantID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, "DELETE FROM sessions WHERE tenant_id = $1 AND user_id = $2", tenantID, userID)
		return err
	})
}

// CreateSession starts a session for a user in one of their tenants.
func (s *Store) CreateSession(ctx context.Context, userID, tenantID string, ttl time.Duration) (token string, sess Session, err error) {
	token, hash, err := auth.Generate(auth.PrefixSession)
	if err != nil {
		return "", Session{}, err
	}
	csrf := make([]byte, 24)
	if _, err := rand.Read(csrf); err != nil {
		return "", Session{}, err
	}
	sess = Session{UserID: userID, TenantID: tenantID, CSRF: hex.EncodeToString(csrf), ExpiresAt: s.now().Add(ttl).UTC()}
	err = s.pool.QueryRow(ctx, `INSERT INTO sessions (token_hash, csrf_token, user_id, tenant_id, expires_at)
		SELECT $1, $2, $3, $4, $5 WHERE EXISTS (SELECT 1 FROM memberships WHERE user_id = $3 AND tenant_id = $4)
		RETURNING id`, hash, sess.CSRF, userID, tenantID, sess.ExpiresAt).Scan(&sess.ID)
	if err != nil {
		return "", Session{}, mapErr(err)
	}
	return token, sess, nil
}

// SessionByToken resolves a session cookie. Sessions of disabled users, or
// for tenants the user no longer belongs to, are not returned. Expiry slides
// forward (at most once a minute) while the session is in use.
func (s *Store) SessionByToken(ctx context.Context, token string, ttl time.Duration) (Session, error) {
	var sess Session
	if _, err := auth.Parse(token, auth.PrefixSession); err != nil {
		return sess, ErrNotFound
	}
	err := s.pool.QueryRow(ctx, `
		UPDATE sessions se SET
			last_seen_at = CASE WHEN se.last_seen_at < now() - interval '1 minute' THEN now() ELSE se.last_seen_at END,
			expires_at   = CASE WHEN se.last_seen_at < now() - interval '1 minute' THEN now() + $2::interval ELSE se.expires_at END
		FROM users u, memberships m
		WHERE se.token_hash = $1 AND se.expires_at > now()
		  AND u.id = se.user_id AND u.disabled_at IS NULL
		  AND m.user_id = se.user_id AND m.tenant_id = se.tenant_id
		RETURNING se.id, se.user_id, u.email, u.name, se.tenant_id, m.role, se.csrf_token, se.expires_at`,
		auth.Hash(token), ttl).
		Scan(&sess.ID, &sess.UserID, &sess.Email, &sess.Name, &sess.TenantID, &sess.Role, &sess.CSRF, &sess.ExpiresAt)
	return sess, mapErr(err)
}

// SwitchSessionTenant moves a session to another tenant the user belongs to.
func (s *Store) SwitchSessionTenant(ctx context.Context, sessionID, userID, tenantID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE sessions SET tenant_id = $3 WHERE id = $1 AND user_id = $2
		AND EXISTS (SELECT 1 FROM memberships WHERE user_id = $2 AND tenant_id = $3)`, sessionID, userID, tenantID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSession ends one session.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE id = $1", id)
	return err
}

// Invitation is a pending invitation to join a tenant.
type Invitation struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	TenantName string    `json:"tenant_name"`
	Email      string    `json:"email"`
	Role       Role      `json:"role"`
	ExpiresAt  time.Time `json:"expires_at"`
	CreatedAt  time.Time `json:"created_at"`
}

// CreateInvitation issues a single-use invitation token.
func (s *Store) CreateInvitation(ctx context.Context, tenantID, email string, role Role, invitedBy *string, ttl time.Duration) (Invitation, string, error) {
	token, hash, err := auth.Generate(auth.PrefixInvite)
	if err != nil {
		return Invitation{}, "", err
	}
	inv := Invitation{TenantID: tenantID, Email: email, Role: role, ExpiresAt: s.now().Add(ttl).UTC()}
	err = s.pool.QueryRow(ctx, `INSERT INTO invitations (tenant_id, email, role, token_hash, invited_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`, tenantID, email, role, hash, invitedBy, inv.ExpiresAt).
		Scan(&inv.ID, &inv.CreatedAt)
	if err != nil {
		return Invitation{}, "", mapErr(err)
	}
	return inv, token, nil
}

// InvitationByToken returns a valid, unaccepted invitation.
func (s *Store) InvitationByToken(ctx context.Context, token string) (Invitation, error) {
	var inv Invitation
	if _, err := auth.Parse(token, auth.PrefixInvite); err != nil {
		return inv, ErrInvalidToken
	}
	err := s.pool.QueryRow(ctx, `SELECT i.id, i.tenant_id, t.name, i.email, i.role, i.expires_at, i.created_at
		FROM invitations i JOIN tenants t ON t.id = i.tenant_id
		WHERE i.token_hash = $1 AND i.accepted_at IS NULL AND i.expires_at > now()`, auth.Hash(token)).
		Scan(&inv.ID, &inv.TenantID, &inv.TenantName, &inv.Email, &inv.Role, &inv.ExpiresAt, &inv.CreatedAt)
	if errors.Is(mapErr(err), ErrNotFound) {
		return inv, ErrInvalidToken
	}
	return inv, err
}

// AcceptInvitation adds userID to the invitation's tenant. The user's email
// must match the invited address.
func (s *Store) AcceptInvitation(ctx context.Context, token, userID string) (Invitation, error) {
	var inv Invitation
	if _, err := auth.Parse(token, auth.PrefixInvite); err != nil {
		return inv, ErrInvalidToken
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT i.id, i.tenant_id, t.name, i.email, i.role, i.expires_at, i.created_at
			FROM invitations i JOIN tenants t ON t.id = i.tenant_id
			JOIN users u ON u.id = $2 AND u.email = i.email
			WHERE i.token_hash = $1 AND i.accepted_at IS NULL AND i.expires_at > now()
			FOR UPDATE OF i`, auth.Hash(token), userID).
			Scan(&inv.ID, &inv.TenantID, &inv.TenantName, &inv.Email, &inv.Role, &inv.ExpiresAt, &inv.CreatedAt)
		if err != nil {
			if errors.Is(mapErr(err), ErrNotFound) {
				return ErrInvalidToken
			}
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE invitations SET accepted_at = now() WHERE id = $1", inv.ID); err != nil {
			return err
		}
		// Never downgrade an existing, higher membership through an invite.
		_, err = tx.Exec(ctx, `INSERT INTO memberships (user_id, tenant_id, role) VALUES ($1, $2, $3)
			ON CONFLICT (user_id, tenant_id) DO UPDATE SET role = CASE
				WHEN memberships.role = 'owner' OR (memberships.role = 'admin' AND EXCLUDED.role = 'viewer') THEN memberships.role
				ELSE EXCLUDED.role END`, userID, inv.TenantID, inv.Role)
		return err
	})
	return inv, err
}

// ListInvitations lists a tenant's pending invitations.
func (s *Store) ListInvitations(ctx context.Context, tenantID string) ([]Invitation, error) {
	rows, err := s.pool.Query(ctx, `SELECT i.id, i.tenant_id, t.name, i.email, i.role, i.expires_at, i.created_at
		FROM invitations i JOIN tenants t ON t.id = i.tenant_id
		WHERE i.tenant_id = $1 AND i.accepted_at IS NULL AND i.expires_at > now() ORDER BY i.created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Invitation, error) {
		var inv Invitation
		err := r.Scan(&inv.ID, &inv.TenantID, &inv.TenantName, &inv.Email, &inv.Role, &inv.ExpiresAt, &inv.CreatedAt)
		return inv, err
	})
}

// RevokeInvitation deletes a pending invitation.
func (s *Store) RevokeInvitation(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM invitations WHERE tenant_id = $1 AND id = $2 AND accepted_at IS NULL", tenantID, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
