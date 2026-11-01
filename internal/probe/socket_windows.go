//go:build windows

package probe

import (
	"errors"
	"net"
)

// Windows probes use the system ICMP helper; these socket paths remain only
// for tests that inject their own sockets.
func listenICMP() (net.PacketConn, bool, error) {
	return nil, false, errors.New("icmp sockets are not used on Windows")
}

func listenTraceUDP() (tracePacketConn, error) {
	return nil, errors.New("udp traceroute is not used on Windows")
}
