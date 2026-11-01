//go:build !windows

package probe

func isRefused(error) bool { return false }

func isUnreachable(error) bool { return false }
