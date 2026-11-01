package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/auth"
	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/internal/testutil/pgtest"
)

func TestUsersSessionsAndRoles(t *testing.T) {
	s := pgtest.New(t)
	acme, _ := s.CreateTenant(ctx, "Acme")
	globex, _ := s.CreateTenant(ctx, "Globex")
	hash, _ := auth.HashPassword("a long enough password")
	alice, err := s.CreateUser(ctx, "alice@msp.example", "Alice", hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "alice@msp.example", "Dup", hash); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate email: %v", err)
	}
	_ = s.AddMembership(ctx, alice.ID, acme.ID, store.RoleOwner)
	_ = s.AddMembership(ctx, alice.ID, globex.ID, store.RoleViewer)

	ms, _ := s.Memberships(ctx, alice.ID)
	if len(ms) != 2 || ms[0].TenantName != "Acme" || ms[1].Role != store.RoleViewer {
		t.Fatalf("memberships: %+v", ms)
	}

	tok, sess, err := s.CreateSession(ctx, alice.ID, acme.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SessionByToken(ctx, tok, time.Hour)
	if err != nil || got.ID != sess.ID || got.Role != store.RoleOwner || got.CSRF == "" || got.Email != "alice@msp.example" {
		t.Fatalf("session lookup: %+v %v", got, err)
	}
	// MSP staff switch tenants; role follows the membership.
	if err := s.SwitchSessionTenant(ctx, sess.ID, alice.ID, globex.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.SessionByToken(ctx, tok, time.Hour); got.TenantID != globex.ID || got.Role != store.RoleViewer {
		t.Fatalf("after switch: %+v", got)
	}
	other, _ := s.CreateTenant(ctx, "Not hers")
	if err := s.SwitchSessionTenant(ctx, sess.ID, alice.ID, other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("switched into a tenant without membership: %v", err)
	}
	if _, _, err := s.CreateSession(ctx, alice.ID, other.ID, time.Hour); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session for non-member tenant: %v", err)
	}

	// The last owner cannot be demoted or removed.
	if err := s.SetMemberRole(ctx, acme.ID, alice.ID, store.RoleAdmin); !errors.Is(err, store.ErrLastOwner) {
		t.Fatalf("demoted last owner: %v", err)
	}
	if err := s.RemoveMember(ctx, acme.ID, alice.ID); !errors.Is(err, store.ErrLastOwner) {
		t.Fatalf("removed last owner: %v", err)
	}

	// Changing the password ends every session.
	if err := s.SetPassword(ctx, alice.ID, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionByToken(ctx, tok, time.Hour); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session survived password change: %v", err)
	}
}

func TestInvitations(t *testing.T) {
	s := pgtest.New(t)
	acme, _ := s.CreateTenant(ctx, "Acme")
	inv, tok, err := s.CreateInvitation(ctx, acme.ID, "bob@acme.example", store.RoleAdmin, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.InvitationByToken(ctx, tok); err != nil || got.ID != inv.ID || got.TenantName != "Acme" {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	hash, _ := auth.HashPassword("another long password")
	mallory, _ := s.CreateUser(ctx, "mallory@evil.example", "", hash)
	if _, err := s.AcceptInvitation(ctx, tok, mallory.ID); !errors.Is(err, store.ErrInvalidToken) {
		t.Fatalf("invitation accepted by a different email: %v", err)
	}
	bob, _ := s.CreateUser(ctx, "bob@acme.example", "Bob", hash)
	if _, err := s.AcceptInvitation(ctx, tok, bob.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.MemberRole(ctx, acme.ID, bob.ID); r != store.RoleAdmin {
		t.Fatalf("role after accept: %q", r)
	}
	if _, err := s.AcceptInvitation(ctx, tok, bob.ID); !errors.Is(err, store.ErrInvalidToken) {
		t.Fatal("invitation reused")
	}
	// A later, lower invitation never downgrades an existing role.
	_, tok2, _ := s.CreateInvitation(ctx, acme.ID, "bob@acme.example", store.RoleViewer, nil, time.Hour)
	_, _ = s.AcceptInvitation(ctx, tok2, bob.ID)
	if r, _ := s.MemberRole(ctx, acme.ID, bob.ID); r != store.RoleAdmin {
		t.Fatalf("invite downgraded role to %q", r)
	}
	_, _, _ = s.CreateInvitation(ctx, acme.ID, "carol@acme.example", store.RoleViewer, nil, -time.Minute)
	if pending, _ := s.ListInvitations(ctx, acme.ID); len(pending) != 0 {
		t.Fatalf("expired invitation listed: %+v", pending)
	}
}
