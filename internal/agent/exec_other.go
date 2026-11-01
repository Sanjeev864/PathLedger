//go:build !unix

package agent

import "errors"

// Exec is not supported on this platform; updates are installed but take
// effect only when the service manager restarts the agent from bin/.
func Exec(string) error {
	return errors.New("self-update is not supported on this platform")
}

// SelfUpdateSupported reports whether this platform can restart into an
// update in place.
const SelfUpdateSupported = false
