package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// defaultStateDir is where the agent keeps its identity and spool unless
// told otherwise.
func defaultStateDir() string {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "PathLedger", "agent")
	case "darwin":
		return "/Library/Application Support/PathLedger/agent"
	default:
		return "/var/lib/pathledger-agent"
	}
}
