// Package gateway owns trusted ingress and protocol flow lifetimes. Credentials
// and protocol adapters remain outside the sandbox engine.
package gateway

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/unix"
)

// OriginalDestination reads the kernel's pre-NAT destination from the first
// accepted TCP socket, before TLS or relay wrapping. Guest headers and the
// socket's source address are never authority. This ingress supports IPv4.
func OriginalDestination(connection *net.TCPConn) (netip.AddrPort, error) {
	if connection == nil {
		return netip.AddrPort{}, fmt.Errorf("accepted TCP socket required")
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var address [16]byte
	length := uint32(len(address))
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, fd, unix.SOL_IP, 80, uintptr(unsafe.Pointer(&address[0])), uintptr(unsafe.Pointer(&length)), 0)
		if errno != 0 {
			socketErr = errno
		}
	})
	if err != nil {
		return netip.AddrPort{}, err
	}
	if socketErr != nil {
		return netip.AddrPort{}, fmt.Errorf("read original destination: %w", socketErr)
	}
	if length != 16 || binary.NativeEndian.Uint16(address[:2]) != unix.AF_INET {
		return netip.AddrPort{}, fmt.Errorf("unsupported original destination family")
	}
	ip := netip.AddrFrom4([4]byte{address[4], address[5], address[6], address[7]})
	port := binary.BigEndian.Uint16(address[2:4])
	if port == 0 || ip.IsUnspecified() {
		return netip.AddrPort{}, fmt.Errorf("kernel original destination is incomplete")
	}
	return netip.AddrPortFrom(ip, port), nil
}

// AcceptDestination refuses direct listener connections and non-reserved
// destinations before a protocol adapter can request application authorization.
func AcceptDestination(connection *net.TCPConn, reserved netip.Prefix) (netip.AddrPort, error) {
	if !reserved.IsValid() || !reserved.Addr().Is4() || !reserved.Addr().IsPrivate() {
		return netip.AddrPort{}, fmt.Errorf("private IPv4 service range required")
	}
	destination, err := OriginalDestination(connection)
	if err != nil {
		return netip.AddrPort{}, err
	}
	if !reserved.Contains(destination.Addr()) {
		return netip.AddrPort{}, fmt.Errorf("connection did not arrive through an assigned service route")
	}
	if actual, ok := connection.LocalAddr().(*net.TCPAddr); ok && netip.AddrPortFrom(actual.AddrPort().Addr().Unmap(), actual.AddrPort().Port()) == destination {
		return netip.AddrPort{}, fmt.Errorf("direct gateway connection refused")
	}
	return destination, nil
}
