//go:build !linux

package wire

import "net"

// queryKernelPMTU is a no-op on platforms without a direct socket-level PMTU query,
// falling back cleanly to interface MTU correlation.
func queryKernelPMTU(conn *net.UDPConn, isV4 bool) (int, error) {
	return 0, nil
}
