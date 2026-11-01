//go:build windows

package probe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no unprivileged ICMP sockets, but its ICMP helper API
// (iphlpapi.dll, the one ping.exe and tracert.exe use) sends echo requests
// with any TTL as an ordinary user. Round-trip times have 1 ms resolution.

var (
	iphlpapi            = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile  = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle = iphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho    = iphlpapi.NewProc("IcmpSendEcho")
)

// IP_STATUS values from ipexport.h.
const (
	ipSuccess              = 0
	ipDestNetUnreachable   = 11002
	ipDestHostUnreachable  = 11003
	ipDestProtUnreachable  = 11004
	ipDestPortUnreachable  = 11005
	ipReqTimedOut          = 11010
	ipTTLExpiredTransit    = 11013
	ipTTLExpiredReassembly = 11014
	ipDestUnreachable      = 11040
)

// ipOptionInformation and winEchoReply mirror the Windows structures; Go's
// natural alignment matches the C layout on 32- and 64-bit targets.
type ipOptionInformation struct {
	TTL, TOS, Flags, OptionsSize uint8
	OptionsData                  uintptr
}

type winEchoReply struct {
	Address       uint32
	Status        uint32
	RoundTripTime uint32
	DataSize      uint16
	Reserved      uint16
	Data          uintptr
	Options       ipOptionInformation
}

type winPinger struct{}

// systemPinger returns the Windows ICMP helper.
func systemPinger() sysPinger { return winPinger{} }

var icmpOnce struct {
	sync.Once
	err error
}

func (winPinger) ping(dst netip.Addr, ttl int, timeout time.Duration, payload []byte) (sysReply, error) {
	icmpOnce.Do(func() { icmpOnce.err = procIcmpSendEcho.Find() })
	if icmpOnce.err != nil {
		return sysReply{}, fmt.Errorf("icmp unavailable: %w", icmpOnce.err)
	}
	h, _, err := procIcmpCreateFile.Call()
	if h == uintptr(windows.InvalidHandle) {
		return sysReply{}, fmt.Errorf("icmp unavailable: IcmpCreateFile: %w", err)
	}
	defer procIcmpCloseHandle.Call(h)

	if len(payload) == 0 {
		payload = []byte{0}
	}
	ip4 := dst.As4()
	opts := ipOptionInformation{TTL: uint8(min(max(ttl, 1), 255))}
	buf := make([]byte, int(unsafe.Sizeof(winEchoReply{}))+len(payload)+8+64)
	ms := uint32(max(timeout.Milliseconds(), 1))
	n, _, callErr := procIcmpSendEcho.Call(h,
		uintptr(binary.LittleEndian.Uint32(ip4[:])),
		uintptr(unsafe.Pointer(&payload[0])), uintptr(len(payload)),
		uintptr(unsafe.Pointer(&opts)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(ms))

	reply := (*winEchoReply)(unsafe.Pointer(&buf[0]))
	status := reply.Status
	if n == 0 {
		var errno syscall.Errno
		if !errors.As(callErr, &errno) {
			return sysReply{}, callErr
		}
		status = uint32(errno)
	}
	var addr [4]byte
	binary.LittleEndian.PutUint32(addr[:], reply.Address)
	r := sysReply{from: netip.AddrFrom4(addr), rtt: time.Duration(reply.RoundTripTime) * time.Millisecond}
	switch status {
	case ipSuccess:
		r.kind = replyEcho
	case ipTTLExpiredTransit, ipTTLExpiredReassembly:
		r.kind = replyTTLExpired
	case ipDestNetUnreachable:
		r.kind, r.unreachable = replyUnreachable, "net"
	case ipDestHostUnreachable, ipDestUnreachable:
		r.kind, r.unreachable = replyUnreachable, "host"
	case ipDestProtUnreachable, ipDestPortUnreachable:
		r.kind, r.unreachable = replyUnreachable, "other"
	case ipReqTimedOut:
		return sysReply{}, errSysTimeout
	default:
		return sysReply{}, fmt.Errorf("icmp: IP status %d", status)
	}
	return r, nil
}
