//go:build !windows

package probe

func systemPinger() sysPinger { return nil }
