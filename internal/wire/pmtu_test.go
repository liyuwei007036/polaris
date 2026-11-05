package wire

import (
	"net"
	"testing"
)

func TestDetectPathMTU(t *testing.T) {
	// Loopback address
	pmtu, err := DetectPathMTU("127.0.0.1:19994")
	if err != nil {
		t.Fatalf("DetectPathMTU(127.0.0.1) failed: %v", err)
	}
	if pmtu < IPv4MinMTU {
		t.Errorf("expected pmtu >= %d, got %d", IPv4MinMTU, pmtu)
	}

	udpMTU, err := DetectUDPMTU("127.0.0.1:19994")
	if err != nil {
		t.Fatalf("DetectUDPMTU(127.0.0.1) failed: %v", err)
	}
	expectedOverhead := IPv4HeaderLen + UDPHeaderLen
	if udpMTU != pmtu-expectedOverhead {
		t.Errorf("expected udpMTU %d, got %d", pmtu-expectedOverhead, udpMTU)
	}
}

func TestInterfaceMTUForIP(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skip("cannot list interfaces")
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
			if ip != nil && !ip.IsLoopback() {
				mtu, err := InterfaceMTUForIP(ip)
				if err != nil {
					t.Fatalf("InterfaceMTUForIP(%s) failed: %v", ip, err)
				}
				if mtu != ifi.MTU {
					t.Errorf("expected MTU %d for %s, got %d", ifi.MTU, ip, mtu)
				}
				return
			}
		}
	}
}
