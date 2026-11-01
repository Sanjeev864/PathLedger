//go:build linux || darwin

package probe

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// listenICMP opens an unprivileged datagram ping socket when the OS allows it
// and otherwise falls back to a raw socket.
func listenICMP() (net.PacketConn, bool, error) {
	if fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_ICMP); err == nil {
		f := os.NewFile(uintptr(fd), "icmp")
		conn, err := net.FilePacketConn(f)
		_ = f.Close() // FilePacketConn dups the descriptor
		if err == nil {
			return conn, false, nil
		}
	}
	conn, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, false, fmt.Errorf("icmp unavailable: allow ping sockets (sysctl net.ipv4.ping_group_range) or grant CAP_NET_RAW: %w", err)
	}
	return conn, true, nil
}
