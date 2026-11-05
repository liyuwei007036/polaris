package wire

import (
	"errors"
	"net"
)

// IP protocol header overhead constants and standard minimum bounds per RFC 791 and RFC 8200.
const (
	IPv4HeaderLen = 20 // RFC 791 IPv4 base header length
	IPv6HeaderLen = 40 // RFC 8200 IPv6 base header length
	UDPHeaderLen  = 8  // RFC 768 UDP header length

	IPv4MinMTU = 576  // RFC 791 minimum MTU guaranteed across all IPv4 internet paths
	IPv6MinMTU = 1280 // RFC 8200 minimum IPv6 link MTU
)

func isIPv4(ip net.IP) bool {
	return ip.To4() != nil
}

// resolveToUDPAddr converts various address representations to a *net.UDPAddr.
func resolveToUDPAddr(addr any) (*net.UDPAddr, error) {
	switch v := addr.(type) {
	case *net.UDPAddr:
		if v == nil {
			return nil, errors.New("nil UDPAddr")
		}
		return v, nil
	case string:
		return net.ResolveUDPAddr("udp", v)
	case net.Addr:
		if v == nil {
			return nil, errors.New("nil net.Addr")
		}
		return net.ResolveUDPAddr("udp", v.String())
	default:
		return nil, errors.New("unsupported address type")
	}
}

// InterfaceMTUForIP returns the MTU of the local network interface assigned to localIP.
func InterfaceMTUForIP(localIP net.IP) (int, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return 0, err
	}
	for _, ifi := range ifaces {
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && ip.Equal(localIP) {
				if ifi.MTU > 0 {
					return ifi.MTU, nil
				}
			}
		}
	}
	return 0, errors.New("no interface found for local IP")
}

// DetectPathMTU discovers the Path MTU (link/IP level) to the given remote address.
// It queries OS kernel routing/socket PMTU discovery where available (Linux),
// and correlates with local interface MTUs.
func DetectPathMTU(raddr any) (int, error) {
	udpAddr, err := resolveToUDPAddr(raddr)
	if err != nil {
		return 0, err
	}

	network := "udp4"
	isV4 := isIPv4(udpAddr.IP)
	if !isV4 {
		network = "udp6"
	}

	conn, err := net.DialUDP(network, nil, udpAddr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	var kernelPMTU int
	if kmtu, err := queryKernelPMTU(conn, isV4); err == nil && kmtu > 0 {
		kernelPMTU = kmtu
	}

	var ifaceMTU int
	if localAddr, ok := conn.LocalAddr().(*net.UDPAddr); ok && localAddr != nil {
		if imtu, err := InterfaceMTUForIP(localAddr.IP); err == nil && imtu > 0 {
			ifaceMTU = imtu
		}
	}

	// Reconcile detected values
	if kernelPMTU > 0 && ifaceMTU > 0 {
		return min(kernelPMTU, ifaceMTU), nil
	}
	if kernelPMTU > 0 {
		return kernelPMTU, nil
	}
	if ifaceMTU > 0 {
		return ifaceMTU, nil
	}
	if isV4 {
		return IPv4MinMTU, nil
	}
	return IPv6MinMTU, nil
}

// DetectUDPMTU discovers the maximum UDP payload size that can be transmitted
// to raddr without IP packet fragmentation or rejection.
func DetectUDPMTU(raddr any) (int, error) {
	udpAddr, err := resolveToUDPAddr(raddr)
	if err != nil {
		return 0, err
	}

	pmtu, err := DetectPathMTU(udpAddr)
	if err != nil || pmtu <= 0 {
		if isIPv4(udpAddr.IP) {
			pmtu = IPv4MinMTU
		} else {
			pmtu = IPv6MinMTU
		}
	}

	var headerOverhead int
	if isIPv4(udpAddr.IP) {
		headerOverhead = IPv4HeaderLen + UDPHeaderLen
	} else {
		headerOverhead = IPv6HeaderLen + UDPHeaderLen
	}

	udpPayloadMTU := pmtu - headerOverhead
	if udpPayloadMTU <= 0 {
		return 0, errors.New("calculated UDP payload MTU is non-positive")
	}
	return udpPayloadMTU, nil
}
