//go:build !linux && !darwin

package probe

import "errors"

func listenTraceUDP() (tracePacketConn, error) {
	return nil, errors.New("traceroute is not supported on this platform yet")
}
