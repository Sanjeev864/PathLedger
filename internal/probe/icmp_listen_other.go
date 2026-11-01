//go:build !linux && !darwin && !windows

package probe

import (
	"fmt"
	"net"
)

// listenICMP uses a raw socket, which needs administrator rights on these
// platforms.
func listenICMP() (net.PacketConn, bool, error) {
	conn, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, false, fmt.Errorf("icmp unavailable: raw sockets need administrator rights: %w", err)
	}
	return conn, true, nil
}
