// Package pgtest gives tests a fresh, migrated PostgreSQL database.
//
// Set PATHLEDGER_TEST_DATABASE_URL to a server URL whose user may create
// databases, e.g. postgres://postgres@127.0.0.1:5432/postgres. Tests that need
// a database are skipped when it is unset.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sanjeev864/pathledger/internal/controller/store"
)

// EnvVar names the admin connection URL.
const EnvVar = "PATHLEDGER_TEST_DATABASE_URL"

// New creates a uniquely named database, migrates it, and drops it when the
// test finishes.
func New(t testing.TB) *store.Store {
	t.Helper()
	admin := os.Getenv(EnvVar)
	if admin == "" {
		t.Skipf("%s not set; skipping database test", EnvVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "pathledger_test_" + hex.EncodeToString(b)

	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connecting to test server: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		conn.Close(ctx)
		t.Fatalf("creating test database: %v", err)
	}
	conn.Close(ctx)

	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := store.Open(ctx, u.String(), log)
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrating test database: %v", err)
	}
	t.Cleanup(func() {
		s.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if c, err := pgx.Connect(ctx, admin); err == nil {
			_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
			c.Close(ctx)
		}
	})
	return s
}
