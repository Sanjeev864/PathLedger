// Command pathledger-controller runs the PathLedger API and provides admin
// commands for database migrations, tenants and API keys.
//
//	pathledger-controller serve
//	pathledger-controller migrate
//	pathledger-controller tenant create -name "Acme Clinics"
//	pathledger-controller apikey create -tenant <tenant-id> -name ops
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sanjeev864/pathledger/internal/buildinfo"
	"github.com/sanjeev864/pathledger/internal/controller/api"
	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/internal/enrich"
	"github.com/sanjeev864/pathledger/internal/logging"
)

const usage = `Usage: pathledger-controller <command> [flags]

Commands:
  serve          run the HTTP API (applies pending migrations first)
  migrate        apply pending database migrations and exit
  tenant create  create a tenant and print its first API key
  apikey create  create an additional API key for a tenant
  version        print the version

Configuration (environment):
  PATHLEDGER_DATABASE_URL  PostgreSQL URL (required)
  PATHLEDGER_LISTEN_ADDR   listen address (default :8080)
  PATHLEDGER_LOG_LEVEL     debug, info, warn, error (default info)
  PATHLEDGER_LOG_FORMAT    json or text (default json)
  PATHLEDGER_ASN_DB        path to IPtoASN ip2asn-v4.tsv(.gz) (optional; see scripts/fetch-asn-db.sh)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	log, err := logging.New(stderr, env("PATHLEDGER_LOG_LEVEL", "info"), env("PATHLEDGER_LOG_FORMAT", "json"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := args[0]
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		cmd += " " + args[1]
		args = args[1:]
	}
	args = args[1:]

	switch cmd {
	case "version":
		fmt.Fprintln(stdout, buildinfo.Version)
		return 0
	case "serve":
		err = withStore(ctx, log, func(st *store.Store) error { return serve(ctx, st, log) })
	case "migrate":
		err = withStore(ctx, log, func(*store.Store) error { return nil })
	case "tenant create":
		err = tenantCreate(ctx, args, stdout, log)
	case "apikey create":
		err = apikeyCreate(ctx, args, stdout, log)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 2
		}
		log.Error("command failed", "command", cmd, "error", err)
		return 1
	}
	return 0
}

// withStore opens the database, applies migrations and runs fn.
func withStore(ctx context.Context, log *slog.Logger, fn func(*store.Store) error) error {
	dsn := os.Getenv("PATHLEDGER_DATABASE_URL")
	if dsn == "" {
		return errors.New("PATHLEDGER_DATABASE_URL is not set")
	}
	openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	st, err := store.Open(openCtx, dsn, log)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(openCtx); err != nil {
		return err
	}
	return fn(st)
}

func serve(ctx context.Context, st *store.Store, log *slog.Logger) error {
	addr := env("PATHLEDGER_LISTEN_ADDR", ":8080")
	if path := os.Getenv("PATHLEDGER_ASN_DB"); path != "" {
		db, err := enrich.LoadFile(path)
		if err != nil {
			return fmt.Errorf("loading ASN data from %s: %w", path, err)
		}
		st.SetASNData(db)
		log.Info("ASN data loaded", "ranges", db.Len())
	} else {
		log.Warn("PATHLEDGER_ASN_DB not set; traceroute hops will not be attributed to networks")
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.New(st, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("controller listening", "addr", addr, "version", buildinfo.Version)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func tenantCreate(ctx context.Context, args []string, stdout io.Writer, log *slog.Logger) error {
	fs := flag.NewFlagSet("tenant create", flag.ContinueOnError)
	name := fs.String("name", "", "tenant (customer) name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return errors.New("-name is required")
	}
	return withStore(ctx, log, func(st *store.Store) error {
		t, err := st.CreateTenant(ctx, strings.TrimSpace(*name))
		if err != nil {
			return err
		}
		_, key, err := st.CreateAPIKey(ctx, t.ID, "initial")
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Tenant created\n  id:      %s\n  name:    %s\n  api key: %s\n\nStore the API key now; it cannot be shown again.\n", t.ID, t.Name, key)
		return nil
	})
}

func apikeyCreate(ctx context.Context, args []string, stdout io.Writer, log *slog.Logger) error {
	fs := flag.NewFlagSet("apikey create", flag.ContinueOnError)
	tenant := fs.String("tenant", "", "tenant ID")
	name := fs.String("name", "", "a label for the key, e.g. dashboard or ops")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tenant == "" || strings.TrimSpace(*name) == "" {
		return errors.New("-tenant and -name are required")
	}
	return withStore(ctx, log, func(st *store.Store) error {
		k, key, err := st.CreateAPIKey(ctx, *tenant, strings.TrimSpace(*name))
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("tenant %s not found", *tenant)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "API key created\n  id:      %s\n  api key: %s\n\nStore the API key now; it cannot be shown again.\n", k.ID, key)
		return nil
	})
}
