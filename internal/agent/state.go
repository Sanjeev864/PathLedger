package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State is the agent's persisted identity. It contains a credential, so it
// is written with owner-only permissions.
type State struct {
	AgentID       string    `json:"agent_id"`
	AgentToken    string    `json:"agent_token"`
	TenantID      string    `json:"tenant_id"`
	SiteID        string    `json:"site_id"`
	ControllerURL string    `json:"controller_url"`
	EnrolledAt    time.Time `json:"enrolled_at"`
}

// ErrNotEnrolled means no state file exists yet.
var ErrNotEnrolled = errors.New("agent is not enrolled")

// LoadState reads the state file at path.
func LoadState(path string) (State, error) {
	var s State
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, ErrNotEnrolled
	}
	if err != nil {
		return s, fmt.Errorf("reading agent state: %w", err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("parsing agent state %s: %w", path, err)
	}
	if s.AgentID == "" || s.AgentToken == "" {
		return s, fmt.Errorf("agent state %s is incomplete", path)
	}
	return s, nil
}

// SaveState atomically writes the state file with mode 0600.
func SaveState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-state-*")
	if err != nil {
		return fmt.Errorf("writing agent state: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
