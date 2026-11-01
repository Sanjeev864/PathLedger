package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"os"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/demo"
	"github.com/sanjeev864/pathledger/internal/controller/store"
)

// demoCmd seeds a simulated customer and/or feeds it live measurements.
func demoCmd(ctx context.Context, args []string, stdout io.Writer, log *slog.Logger) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	tenant := fs.String("tenant", "", "feed an existing demo tenant instead of creating a new one")
	days := fs.Int("days", 14, fmt.Sprintf("days of history to create (1-%d)", demo.MaxDays))
	email := fs.String("email", "demo@pathledger.example", "sign-in email for the demo owner (password from PATHLEDGER_PASSWORD, or generated)")
	fault := fs.String("fault", "", "inject a live fault: isp, local, upstream, dns, app or offline")
	site := fs.Int("site", 1, fmt.Sprintf("site that gets the fault (1-%d)", len(demo.Sites)))
	after := fs.Duration("after", 2*time.Minute, "start the fault this long after the feed starts")
	dur := fs.Duration("for", 8*time.Minute, "how long the fault lasts")
	seedOnly := fs.Bool("seed-only", false, "create the history and exit without a live feed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	live := demo.LiveOptions{Site: *site - 1, After: *after, For: *dur}
	if *fault != "" {
		f, err := demo.ParseFault(*fault)
		if err != nil {
			return err
		}
		live.Fault = f
	}
	if live.Site < 0 || live.Site >= len(demo.Sites) {
		return fmt.Errorf("-site must be between 1 and %d", len(demo.Sites))
	}
	if *after < 0 || *dur <= 0 {
		return errors.New("-after must be >= 0 and -for > 0")
	}
	if _, err := mail.ParseAddress(*email); err != nil {
		return errors.New("-email must be a valid address")
	}

	return withStore(ctx, log, func(st *store.Store) error {
		if err := demo.PrepareStore(st); err != nil {
			return err
		}
		id := *tenant
		if id == "" {
			s, err := demo.Seed(ctx, st, demo.Options{Days: *days, Email: *email, Password: os.Getenv("PATHLEDGER_PASSWORD")}, log)
			if err != nil {
				return err
			}
			id = s.TenantID
			fmt.Fprintf(stdout, "Demo customer created: %s\n  tenant:   %s\n  sites:    %d, history: %d days, %d measurements, %d incidents\n  sign in:  %s\n",
				demo.TenantName, s.TenantID, len(demo.Sites), *days, s.Results, s.Events, s.Email)
			if s.Password != "" {
				fmt.Fprintf(stdout, "  password: %s   (shown once)\n", s.Password)
			} else {
				fmt.Fprintln(stdout, "  password: unchanged (existing account)")
			}
		}
		if *seedOnly {
			return nil
		}
		fmt.Fprintf(stdout, "Feeding live measurements every few seconds; keep `serve` running and press Ctrl-C to stop.\n")
		if live.Fault != demo.Healthy {
			fmt.Fprintf(stdout, "Fault %q at %s starts in %s and lasts %s.\n", live.Fault, demo.Sites[live.Site].Name, *after, *dur)
		}
		fmt.Fprintf(stdout, "Resume later with: pathledger-controller demo -tenant %s\n", id)
		return demo.Live(ctx, st, id, live, log)
	})
}
