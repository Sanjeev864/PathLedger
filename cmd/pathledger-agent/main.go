// Command pathledger-agent runs PathLedger's network probes at a branch site
// and ships the results to a PathLedger controller.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/sanjeev864/pathledger/internal/agent"
	"github.com/sanjeev864/pathledger/internal/buildinfo"
	"github.com/sanjeev864/pathledger/internal/logging"
	"github.com/sanjeev864/pathledger/internal/update"
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
	stateDir := fs.String("state-dir", env("PATHLEDGER_STATE_DIR", defaultStateDir()), "directory for identity and spooled results (env PATHLEDGER_STATE_DIR)")
	logLevel := fs.String("log-level", env("PATHLEDGER_LOG_LEVEL", "info"), "debug, info, warn or error (env PATHLEDGER_LOG_LEVEL)")
	logFormat := fs.String("log-format", env("PATHLEDGER_LOG_FORMAT", "json"), "json or text (env PATHLEDGER_LOG_FORMAT)")
	updateKey := fs.String("update-key", env("PATHLEDGER_UPDATE_PUBLIC_KEY", ""), "base64 ed25519 public key(s), comma-separated, trusted to sign agent releases; empty disables automatic updates (env PATHLEDGER_UPDATE_PUBLIC_KEY)")
	logFile := fs.String("log-file", env("PATHLEDGER_LOG_FILE", ""), "write logs to this file instead of standard error, rotated at 10 MB (env PATHLEDGER_LOG_FILE)")
	enrollOnly := fs.Bool("enroll-only", false, "enrol (if needed) and exit; used by the installers so the enrolment token is never stored")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Println(buildinfo.Version)
		return 0
	}
	var out io.Writer = os.Stderr
	if *logFile != "" {
		f, err := openLog(*logFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "opening log file:", err)
			return 2
		}
		defer f.Close()
		out = f
	}
	log, err := logging.New(out, *logLevel, *logFormat)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	keys, err := update.ParsePublicKeys(*updateKey)
	if err != nil {
		log.Error("invalid update key", "error", err)
		return 2
	}
	if len(keys) > 0 && !agent.SelfUpdateSupported {
		log.Warn("automatic updates are not supported on this platform; update the agent by reinstalling it")
		keys = nil
	}
	// Run a newer, verified release installed by a previous self-update.
	if os.Getenv(agent.LaunchedEnv) == "" {
		if path := agent.Launch(*stateDir, buildinfo.Version, keys, log); path != "" {
			log.Info("starting installed agent update", "path", path)
			err := agent.Exec(path)
			log.Error("could not start installed update; continuing with this version", "error", err)
		}
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
		UpdateKeys:    keys,
	}
	a, err := agent.New(ctx, cfg, log)
	if err != nil {
		log.Error("agent could not start", "error", err)
		return 1
	}
	if *enrollOnly {
		if err := a.Close(); err != nil {
			log.Error("closing agent state", "error", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "enrolled as agent", a.ID())
		return 0
	}
	err = a.Run(ctx)
	if errors.Is(err, agent.ErrUpdateInstalled) {
		stop()
		if path := agent.Launch(*stateDir, buildinfo.Version, keys, log); path != "" {
			log.Info("restarting into agent update", "path", path)
			err = agent.Exec(path)
		}
		// Exec failed or the update was rejected at launch: exit non-zero so
		// the service manager restarts the agent (and its launcher logic).
		log.Error("could not start the installed update", "error", err)
		return 1
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("agent stopped with error", "error", err)
		return 1
	}
	return 0
}
