//go:build linux

package wire

import (
	"net"

	"golang.org/x/sys/unix"
)

// queryKernelPMTU queries the Linux kernel's route cache / PMTU for the destination UDP address.
func queryKernelPMTU(conn *net.UDPConn, isV4 bool) (int, error) {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pmtu int
	var sockErr error
	err = rawConn.Control(func(fd uintptr) {
		if isV4 {
			_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_DO)
			pmtu, sockErr = unix.GetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MTU)
		} else {
			_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_MTU_DISCOVER, unix.IPV6_PMTUDISC_DO)
			pmtu, sockErr = unix.GetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_MTU)
		}
	})
	if err != nil {
		return 0, err
	}
	if sockErr != nil {
		return 0, sockErr
	}
	return pmtu, nil
}
