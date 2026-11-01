//go:build linux || darwin

package probe

import (
	"fmt"
	"net"
	"syscall"
)

type ttlUDPConn struct{ *net.UDPConn }

// SetTTL sets the IPv4 TTL for subsequent datagrams.
func (c ttlUDPConn) SetTTL(ttl int) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := raw.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL, ttl)
	}); err != nil {
		return err
	}
	return serr
}

func listenTraceUDP() (tracePacketConn, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, fmt.Errorf("opening traceroute socket: %w", err)
	}
	return ttlUDPConn{c}, nil
}
