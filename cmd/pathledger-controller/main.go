// Command pathledger-controller runs the PathLedger API and provides admin
// commands for database migrations, tenants and API keys.
//
//	pathledger-controller serve
//	pathledger-controller migrate
//	pathledger-controller tenant create -name "Acme Clinics"
//	pathledger-controller apikey create -tenant <tenant-id> -name ops
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sanjeev864/pathledger/internal/buildinfo"
	"github.com/sanjeev864/pathledger/internal/controller/alert"
	"github.com/sanjeev864/pathledger/internal/controller/analytics"
	"github.com/sanjeev864/pathledger/internal/controller/api"
	"github.com/sanjeev864/pathledger/internal/controller/auth"
	"github.com/sanjeev864/pathledger/internal/controller/maintenance"
	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/internal/enrich"
	"github.com/sanjeev864/pathledger/internal/logging"
	"github.com/sanjeev864/pathledger/internal/metrics"
)

const usage = `Usage: pathledger-controller <command> [flags]

Commands:
  serve          run the HTTP API (applies pending migrations first)
  migrate        apply pending database migrations and exit
  tenant create  create a tenant and print its first API key
  apikey create  create an additional API key for a tenant
  user create    create a dashboard user and add them to a tenant
                 (password from PATHLEDGER_PASSWORD, or the first line of stdin)
  version        print the version

Configuration (environment):
  PATHLEDGER_DATABASE_URL  PostgreSQL URL (required)
  PATHLEDGER_LISTEN_ADDR   listen address (default :8080)
  PATHLEDGER_LOG_LEVEL     debug, info, warn, error (default info)
  PATHLEDGER_LOG_FORMAT    json or text (default json)
  PATHLEDGER_ASN_DB        path to IPtoASN ip2asn-v4.tsv(.gz) (optional; see scripts/fetch-asn-db.sh)
  PATHLEDGER_TRUST_PROXY   "true" when behind a reverse proxy, so rate limits use X-Forwarded-For
  PATHLEDGER_ANALYTICS_WINDOW, _INTERVAL   evaluation window (5m) and frequency (1m)
  PATHLEDGER_RETENTION_DAYS   days of raw results to keep (default 90)
  PATHLEDGER_METRICS_ADDR     address for Prometheus /metrics, e.g. 127.0.0.1:9090 (off by default)
  PATHLEDGER_PUBLIC_URL    dashboard URL used for links in notifications (optional)
  PATHLEDGER_SMTP_HOST, _PORT (587), _USERNAME, _PASSWORD, _FROM   email notifications (optional)
  PATHLEDGER_TELEGRAM_BOT_TOKEN                                    Telegram notifications (optional)
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
	case "user create":
		err = userCreate(ctx, args, os.Stdin, stdout, log)
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
	senders, err := alertSenders(log)
	if err != nil {
		return err
	}
	allowPrivate := os.Getenv("PATHLEDGER_WEBHOOK_ALLOW_PRIVATE") == "true"
	senders["webhook"] = &alert.Webhook{Client: alert.SafeHTTPClient(allowPrivate)}
	notifier := &alert.Notifier{Store: st, Log: log, PublicURL: os.Getenv("PATHLEDGER_PUBLIC_URL")}

	reg := metrics.NewRegistry()
	reg.GaugeSet(map[string]string{
		"pathledger_agents":                     "Active (unrevoked) agents.",
		"pathledger_agents_stale":               "Active agents silent for more than 5 minutes.",
		"pathledger_events_open":                "Open events across all tenants.",
		"pathledger_deliveries_pending":         "Notifications waiting to be sent.",
		"pathledger_deliveries_failed_last_day": "Notifications that failed permanently in the last 24 hours.",
		"pathledger_results_rows_estimate":      "Approximate rows in the results table.",
	}, st.Gauges)

	retention, err := retentionFromEnv()
	if err != nil {
		return err
	}
	go maintenance.Run(ctx, st, retention, time.Hour, log)

	// Background workers stop when ctx is cancelled (on shutdown).
	engine := analytics.NewEngine(st, notifier, log)
	engine.RegisterMetrics(reg)
	every := time.Minute
	if v := os.Getenv("PATHLEDGER_ANALYTICS_WINDOW"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 10*time.Second {
			return fmt.Errorf("PATHLEDGER_ANALYTICS_WINDOW must be a duration of at least 10s")
		}
		engine.SetWindow(d)
	}
	if v := os.Getenv("PATHLEDGER_ANALYTICS_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			return fmt.Errorf("PATHLEDGER_ANALYTICS_INTERVAL must be a duration of at least 1s")
		}
		every = d
	}
	go engine.Run(ctx, every)
	go (&alert.Dispatcher{Store: st, Sender: senders, Log: log,
		Outcomes: reg.CounterVec("pathledger_notifications_total", "Notification delivery attempts by channel kind and outcome.", false, "kind", "outcome"),
	}).Run(ctx, 10*time.Second)

	if maddr := os.Getenv("PATHLEDGER_METRICS_ADDR"); maddr != "" {
		msrv := &http.Server{Addr: maddr, Handler: reg.Handler(), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := msrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("metrics listener failed", "error", err)
			}
		}()
		defer msrv.Close()
		log.Info("metrics listening", "addr", maddr)
	}

	opts := []api.Option{api.WithMetrics(reg)}
	if u := os.Getenv("PATHLEDGER_PUBLIC_URL"); u != "" {
		opts = append(opts, api.WithPublicURL(u))
	}
	if e, ok := senders["email"].(*alert.Email); ok {
		opts = append(opts, api.WithMailer(e))
	}
	if os.Getenv("PATHLEDGER_TRUST_PROXY") == "true" {
		opts = append(opts, api.TrustProxyHeaders())
	}
	if allowPrivate {
		opts = append(opts, api.AllowHTTPWebhooks())
		log.Warn("PATHLEDGER_WEBHOOK_ALLOW_PRIVATE is set: webhooks may target private addresses (development only)")
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.New(st, log, opts...).Handler(),
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

// retentionFromEnv reads PATHLEDGER_RETENTION_DAYS (raw results, default
// 90); resolved events are kept a year and deliveries 30 days.
func retentionFromEnv() (store.Retention, error) {
	days := 90
	if v := os.Getenv("PATHLEDGER_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 7 || n > 3650 {
			return store.Retention{}, errors.New("PATHLEDGER_RETENTION_DAYS must be between 7 and 3650")
		}
		days = n
	}
	return store.Retention{
		Results:    time.Duration(days) * 24 * time.Hour,
		Events:     365 * 24 * time.Hour,
		Deliveries: 30 * 24 * time.Hour,
	}, nil
}

// alertSenders configures email and Telegram from the environment. Either
// may be absent; channels of that kind then fail with a clear error.
func alertSenders(log *slog.Logger) (alert.Router, error) {
	r := alert.Router{}
	if host := os.Getenv("PATHLEDGER_SMTP_HOST"); host != "" {
		port, err := strconv.Atoi(env("PATHLEDGER_SMTP_PORT", "587"))
		if err != nil {
			return nil, fmt.Errorf("PATHLEDGER_SMTP_PORT: %w", err)
		}
		from := os.Getenv("PATHLEDGER_SMTP_FROM")
		if from == "" {
			return nil, errors.New("PATHLEDGER_SMTP_FROM is required when PATHLEDGER_SMTP_HOST is set")
		}
		r["email"] = &alert.Email{Config: alert.SMTPConfig{
			Host: host, Port: port, From: from,
			Username: os.Getenv("PATHLEDGER_SMTP_USERNAME"), Password: os.Getenv("PATHLEDGER_SMTP_PASSWORD"),
		}}
	} else {
		log.Warn("PATHLEDGER_SMTP_HOST not set; email notifications are disabled")
	}
	if tok := os.Getenv("PATHLEDGER_TELEGRAM_BOT_TOKEN"); tok != "" {
		r["telegram"] = &alert.Telegram{Token: tok, Client: &http.Client{Timeout: 15 * time.Second}}
	} else {
		log.Warn("PATHLEDGER_TELEGRAM_BOT_TOKEN not set; Telegram notifications are disabled")
	}
	return r, nil
}

func userCreate(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, log *slog.Logger) error {
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	email := fs.String("email", "", "sign-in email")
	name := fs.String("name", "", "display name")
	tenant := fs.String("tenant", "", "tenant ID to join")
	role := fs.String("role", "owner", "owner, admin or viewer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	addr, err := mail.ParseAddress(*email)
	if err != nil || *tenant == "" {
		return errors.New("-email (valid address) and -tenant are required")
	}
	r := store.Role(*role)
	if !r.Valid() {
		return errors.New("-role must be owner, admin or viewer")
	}
	pw := os.Getenv("PATHLEDGER_PASSWORD")
	if pw == "" {
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		pw = strings.TrimRight(line, "\r\n")
	}
	if err := auth.CheckPasswordPolicy(pw); err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	return withStore(ctx, log, func(st *store.Store) error {
		u, err := st.CreateUser(ctx, strings.ToLower(addr.Address), strings.TrimSpace(*name), hash)
		if errors.Is(err, store.ErrConflict) {
			if u, err = st.UserByEmail(ctx, strings.ToLower(addr.Address)); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "User exists; adding the membership only (password unchanged).")
		} else if err != nil {
			return err
		}
		if err := st.AddMembership(ctx, u.ID, *tenant, r); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("tenant %s not found", *tenant)
			}
			return err
		}
		fmt.Fprintf(stdout, "User %s is now %s of tenant %s.\n", u.Email, r, *tenant)
		return nil
	})
}
