// Package logging configures structured logging for PathLedger binaries.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// New returns a JSON (default) or text logger at the given level
// ("debug", "info", "warn" or "error").
func New(w io.Writer, level, format string) (*slog.Logger, error) {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(strings.ToUpper(level))); err != nil {
		return nil, fmt.Errorf("invalid log level %q", level)
	}
	opts := &slog.HandlerOptions{Level: lv}
	switch strings.ToLower(format) {
	case "", "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("invalid log format %q (use json or text)", format)
	}
}
