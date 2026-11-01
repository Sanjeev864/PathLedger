//go:build windows

package probe

import (
	"errors"

	"golang.org/x/sys/windows"
)

// Winsock reports these with its own error numbers.
func isRefused(err error) bool { return errors.Is(err, windows.WSAECONNREFUSED) }

func isUnreachable(err error) bool {
	return errors.Is(err, windows.WSAENETUNREACH) || errors.Is(err, windows.WSAEHOSTUNREACH)
}
