// Command pathledger-agent runs PathLedger's network probes at a branch site
// and ships the results to a PathLedger controller.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sanjeev864/pathledger/internal/agent"
	"github.com/sanjeev864/pathledger/internal/buildinfo"
	"github.com/sanjeev864/pathledger/internal/logging"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// env returns the environment variable or a default.
func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func run(args []string) int {
	fs := flag.NewFlagSet("pathledger-agent", flag.ContinueOnError)
	controller := fs.String("controller", env("PATHLEDGER_CONTROLLER_URL", ""), "controller base URL, e.g. https://pathledger.example.com (env PATHLEDGER_CONTROLLER_URL)")
	stateDir := fs.String("state-dir", env("PATHLEDGER_STATE_DIR", "/var/lib/pathledger-agent"), "directory for identity and spooled results (env PATHLEDGER_STATE_DIR)")
	logLevel := fs.String("log-level", env("PATHLEDGER_LOG_LEVEL", "info"), "debug, info, warn or error (env PATHLEDGER_LOG_LEVEL)")
	logFormat := fs.String("log-format", env("PATHLEDGER_LOG_FORMAT", "json"), "json or text (env PATHLEDGER_LOG_FORMAT)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Println(buildinfo.Version)
		return 0
	}
	log, err := logging.New(os.Stderr, *logLevel, *logFormat)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The enrolment token is read only from the environment so it never
	// appears in process listings or shell history.
	cfg := agent.Config{
		ControllerURL: *controller,
		EnrollToken:   os.Getenv("PATHLEDGER_ENROLL_TOKEN"),
		StateDir:      *stateDir,
		Version:       buildinfo.Version,
	}
	a, err := agent.New(ctx, cfg, log)
	if err != nil {
		log.Error("agent could not start", "error", err)
		return 1
	}
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("agent stopped with error", "error", err)
		return 1
	}
	return 0
}
