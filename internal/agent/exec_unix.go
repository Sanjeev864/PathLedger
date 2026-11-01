//go:build unix

package agent

import (
	"os"
	"syscall"
)

// Exec replaces the current process with the binary at path, keeping the
// arguments and environment and marking it as launched.
func Exec(path string) error {
	return syscall.Exec(path, append([]string{path}, os.Args[1:]...), append(os.Environ(), LaunchedEnv+"=1"))
}
