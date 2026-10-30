package agent

import (
	"context"
	"net"
	"sort"
	"sync"
	"time"
)

var (
	probeMu          sync.RWMutex
	ipv4ProbeTargets = []string{"223.5.5.5:53", "1.1.1.1:53", "8.8.8.8:53", "119.29.29.29:53", "1.1.1.1:80"}
	ipv6ProbeTargets = []string{"[2400:3200::1]:53", "[2606:4700:4700::1111]:53", "[2001:4860:4860::8888]:53", "[2606:4700:4700::1111]:80"}
)

// SetProbeTargetsForTest configures external probe targets for unit tests.
func SetProbeTargetsForTest(v4, v6 []string) {
	probeMu.Lock()
	defer probeMu.Unlock()
	ipv4ProbeTargets = v4
	ipv6ProbeTargets = v6
}

func getProbeTargets(isIPv4 bool) (string, []string) {
	probeMu.RLock()
	defer probeMu.RUnlock()
	if isIPv4 {
		targets := make([]string, len(ipv4ProbeTargets))
		copy(targets, ipv4ProbeTargets)
		return "tcp4", targets
	}
	targets := make([]string, len(ipv6ProbeTargets))
	copy(targets, ipv6ProbeTargets)
	return "tcp6", targets
}

var (
	egressCacheMu      sync.RWMutex
	egressCacheIPs     []string
	egressCacheExpires time.Time
)

// ClearEgressIPCacheForTest flushes the cached verified IPs.
func ClearEgressIPCacheForTest() {
	egressCacheMu.Lock()
	defer egressCacheMu.Unlock()
	egressCacheIPs = nil
	egressCacheExpires = time.Time{}
}

// CanAccessInternet verifies that outgoing TCP traffic originating from localIP
// can connect to at least one external public probe target.
func CanAccessInternet(ctx context.Context, localIP string) bool {
	ip := net.ParseIP(localIP)
	if ip == nil {
		return false
	}
	isIPv4 := ip.To4() != nil
	network, targets := getProbeTargets(isIPv4)
	if len(targets) == 0 {
		return false
	}

	dialer := &net.Dialer{
		LocalAddr: &net.TCPAddr{IP: ip},
		Timeout:   1200 * time.Millisecond,
	}

	for _, target := range targets {
		conn, err := dialer.DialContext(ctx, network, target)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

// CollectLocalIPs returns all host IP addresses that are active, non-loopback,
// non-link-local, and verified to be capable of accessing the external internet.
func CollectLocalIPs() []string {
	egressCacheMu.RLock()
	if time.Now().Before(egressCacheExpires) && len(egressCacheIPs) > 0 {
		cached := make([]string, len(egressCacheIPs))
		copy(cached, egressCacheIPs)
		egressCacheMu.RUnlock()
		return cached
	}
	egressCacheMu.RUnlock()

	candidates := EnumerateHostCandidateIPs()
	if len(candidates) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var verified []string
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, cand := range candidates {
		wg.Add(1)
		go func(ipStr string) {
			defer wg.Done()
			if CanAccessInternet(ctx, ipStr) {
				mu.Lock()
				verified = append(verified, ipStr)
				mu.Unlock()
			}
		}(cand)
	}
	wg.Wait()

	sort.SliceStable(verified, func(i, j int) bool {
		ipA := net.ParseIP(verified[i])
		ipB := net.ParseIP(verified[j])
		is4A := ipA != nil && ipA.To4() != nil
		is4B := ipB != nil && ipB.To4() != nil
		if is4A != is4B {
			return is4A // IPv4 comes before IPv6
		}
		return verified[i] < verified[j]
	})

	egressCacheMu.Lock()
	egressCacheIPs = verified
	egressCacheExpires = time.Now().Add(45 * time.Second)
	egressCacheMu.Unlock()

	return verified
}

// EnumerateHostCandidateIPs returns all candidate unicast non-loopback IPs on UP interfaces.
func EnumerateHostCandidateIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var ips []string
	seen := make(map[string]bool)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
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
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
				continue
			}
			ipStr := ip.String()
			if !seen[ipStr] {
				seen[ipStr] = true
				ips = append(ips, ipStr)
			}
		}
	}
	return ips
}
