package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	probeMu          sync.RWMutex
	ipv4ProbeTargets = []string{"223.5.5.5:53", "1.1.1.1:53", "8.8.8.8:53", "119.29.29.29:53", "1.1.1.1:80"}
	ipv6ProbeTargets = []string{"[2400:3200::1]:53", "[2606:4700:4700::1111]:53", "[2001:4860:4860::8888]:53", "[2606:4700:4700::1111]:80"}

	echoURLsMu       sync.RWMutex
	ipv4EchoURLsTest []string
	ipv6EchoURLsTest []string
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

// SetEchoURLsForTest configures public IP echo URLs for testing.
func SetEchoURLsForTest(v4, v6 []string) {
	echoURLsMu.Lock()
	defer echoURLsMu.Unlock()
	ipv4EchoURLsTest = v4
	ipv6EchoURLsTest = v6
}

func getEchoURLs(isIPv4 bool) []string {
	echoURLsMu.RLock()
	defer echoURLsMu.RUnlock()
	if isIPv4 {
		if len(ipv4EchoURLsTest) > 0 {
			out := make([]string, len(ipv4EchoURLsTest))
			copy(out, ipv4EchoURLsTest)
			return out
		}
		return []string{
			"http://1.1.1.1/cdn-cgi/trace",
			"http://api.ipify.org",
			"http://icanhazip.com",
			"https://api-ipv4.ip.sb/ip",
		}
	}
	if len(ipv6EchoURLsTest) > 0 {
		out := make([]string, len(ipv6EchoURLsTest))
		copy(out, ipv6EchoURLsTest)
		return out
	}
	return []string{
		"http://[2606:4700:4700::1111]/cdn-cgi/trace",
		"http://api64.ipify.org",
		"http://icanhazip.com",
		"https://api-ipv6.ip.sb/ip",
	}
}

var (
	egressCacheMu       sync.RWMutex
	egressCachePublic   []string
	egressCacheMappings map[string]string
	egressCacheExpires  time.Time
)

// ClearEgressIPCacheForTest flushes the cached verified IPs.
func ClearEgressIPCacheForTest() {
	egressCacheMu.Lock()
	defer egressCacheMu.Unlock()
	egressCachePublic = nil
	egressCacheMappings = nil
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

// ResolvePublicIP queries external echo endpoints originating from localIP in parallel.
// If successful, it returns the public IP that external servers observe.
func ResolvePublicIP(ctx context.Context, localIP string) (string, error) {
	ip := net.ParseIP(localIP)
	if ip == nil {
		return "", errors.New("invalid local IP")
	}
	isIPv4 := ip.To4() != nil
	urls := getEchoURLs(isIPv4)
	if len(urls) == 0 {
		return "", errors.New("no echo URLs configured")
	}

	dialer := &net.Dialer{
		LocalAddr: &net.TCPAddr{IP: ip},
		Timeout:   600 * time.Millisecond,
	}
	transport := &http.Transport{
		DialContext:         dialer.DialContext,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 600 * time.Millisecond,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   900 * time.Millisecond,
	}

	reqCtx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()

	resultCh := make(chan string, len(urls))
	for _, u := range urls {
		go func(targetURL string) {
			req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, targetURL, nil)
			if err != nil {
				resultCh <- ""
				return
			}
			req.Header.Set("User-Agent", "sb-control-probe/1.0")
			resp, err := client.Do(req)
			if err != nil {
				resultCh <- ""
				return
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK {
				resultCh <- ""
				return
			}
			raw := string(body)
			if strings.Contains(targetURL, "/cdn-cgi/trace") {
				for _, line := range strings.Split(raw, "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "ip=") {
						parsed := strings.TrimSpace(strings.TrimPrefix(line, "ip="))
						if p := net.ParseIP(parsed); p != nil {
							resultCh <- p.String()
							return
						}
					}
				}
			} else {
				trimmed := strings.TrimSpace(raw)
				if p := net.ParseIP(trimmed); p != nil {
					resultCh <- p.String()
					return
				}
			}
			resultCh <- ""
		}(u)
	}

	for i := 0; i < len(urls); i++ {
		select {
		case res := <-resultCh:
			if res != "" {
				cancel()
				return res, nil
			}
		case <-reqCtx.Done():
			return "", errors.New("echo endpoints timed out")
		}
	}
	return "", errors.New("echo endpoints unreachable from local IP")
}

// CollectLocalIPs returns all public egress IPs.
func CollectLocalIPs() []string {
	ips, _ := CollectLocalIPsAndMappings()
	return ips
}

// CollectLocalIPMappings returns the public IP -> local interface bind IP mapping.
func CollectLocalIPMappings() map[string]string {
	_, mappings := CollectLocalIPsAndMappings()
	return mappings
}

// CollectLocalIPsAndMappings returns verified public egress IPs and their
// respective local bind IP address mapping (publicIP -> localBindIP).
func CollectLocalIPsAndMappings() ([]string, map[string]string) {
	egressCacheMu.RLock()
	if time.Now().Before(egressCacheExpires) && len(egressCachePublic) > 0 {
		cachedIPs := make([]string, len(egressCachePublic))
		copy(cachedIPs, egressCachePublic)
		cachedMappings := make(map[string]string, len(egressCacheMappings))
		for k, v := range egressCacheMappings {
			cachedMappings[k] = v
		}
		egressCacheMu.RUnlock()
		return cachedIPs, cachedMappings
	}
	egressCacheMu.RUnlock()

	candidates := EnumerateHostCandidateIPs()
	if len(candidates) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	var mu sync.Mutex
	var wg sync.WaitGroup
	seenPublic := make(map[string]bool)
	var verified []string
	mappings := make(map[string]string)

	for _, cand := range candidates {
		wg.Add(1)
		go func(localIPStr string) {
			defer wg.Done()

			// 1. First attempt to discover the real public IP from this local candidate interface.
			pubIP, err := ResolvePublicIP(ctx, localIPStr)
			if err == nil && pubIP != "" {
				mu.Lock()
				if !seenPublic[pubIP] {
					seenPublic[pubIP] = true
					verified = append(verified, pubIP)
					mappings[pubIP] = localIPStr
				}
				mu.Unlock()
				return
			}

			// 2. If ResolvePublicIP fails, but the local IP is already a public non-private IP
			// and CanAccessInternet succeeds, keep it.
			parsed := net.ParseIP(localIPStr)
			if parsed != nil && !parsed.IsPrivate() && CanAccessInternet(ctx, localIPStr) {
				mu.Lock()
				if !seenPublic[localIPStr] {
					seenPublic[localIPStr] = true
					verified = append(verified, localIPStr)
					mappings[localIPStr] = localIPStr
				}
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
	egressCachePublic = verified
	egressCacheMappings = mappings
	egressCacheExpires = time.Now().Add(45 * time.Second)
	egressCacheMu.Unlock()

	return verified, mappings
}

func isIgnoredInterface(name string) bool {
	lower := strings.ToLower(name)
	prefixes := []string{
		"docker",
		"br-",
		"veth",
		"virbr",
		"cni",
		"flannel",
		"calico",
		"tun",
		"tap",
		"wg",
		"tailscale",
		"dummy",
		"lo",
		"bond_slave",
		"kube",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// EnumerateHostCandidateIPs returns candidate local IPs on UP physical interfaces.
// Virtual interfaces (Docker, bridges, veth, wireguard, tunnels) and loopback are excluded.
func EnumerateHostCandidateIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var ips []string
	seen := make(map[string]bool)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		if isIgnoredInterface(iface.Name) {
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
			// Exclude common docker bridge subnets (172.17.0.0/16 - 172.31.0.0/16 where last octet is .1)
			if ip4 := ip.To4(); ip4 != nil {
				if ip4[0] == 172 && ip4[1] >= 17 && ip4[1] <= 31 && ip4[3] == 1 {
					continue
				}
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
