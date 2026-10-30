package wire

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	// CityResolver is a dynamic hook (set by the control plane using ip2region or remote IP database)
	// to resolve IP addresses to their true geographic city/region without relying on hardcoded IP prefixes.
	CityResolver func(ip string) string

	// ASNResolver is a dynamic hook to lookup the BGP ASN for an IP address.
	ASNResolver func(ip string) string

	// IsDomesticResolver is a dynamic hook to check if an IP is in Mainland China.
	IsDomesticResolver func(ip string) bool

	reASN = regexp.MustCompile(`(?i)\bAS(\d+)\b`)

	ipInfoCache sync.Map // map[string]IPInfo
	ipHTTPClient = &http.Client{Timeout: 2500 * time.Millisecond}

	carrierNames = map[string]string{
		"AS4809":  "电信CN2",
		"AS4134":  "电信163",
		"AS23764": "电信CTGNet",
		"AS9929":  "联通A网",
		"AS10099": "联通CUG",
		"AS4837":  "联通169",
		"AS58807": "移动CMIN2",
		"AS58453": "移动CMI",
		"AS9808":  "移动骨干",
		"AS174":   "Cogent",
		"AS45090": "腾讯云",
		"AS37963": "阿里云",
		"AS13335": "Cloudflare",
		"AS16509": "AWS",
		"AS24940": "Hetzner",
		"AS8075":  "Azure",
		"AS2914":  "NTT",
		"AS1299":  "Arelion",
		"AS6939":  "HE",
	}
)

type IPInfo struct {
	IP      string
	ASN     string
	ASName  string
	ISP     string
	City    string
	Country string
}

// LookupIPInfo queries dynamic IP database / API and caches results in memory.
// It never hardcodes static IP prefixes.
func LookupIPInfo(ipStr string) IPInfo {
	ipStr = strings.TrimSpace(ipStr)
	if ipStr == "" {
		return IPInfo{}
	}
	if parsed := net.ParseIP(ipStr); parsed == nil || parsed.IsPrivate() || parsed.IsLoopback() || parsed.IsLinkLocalUnicast() {
		return IPInfo{}
	}

	if val, ok := ipInfoCache.Load(ipStr); ok {
		return val.(IPInfo)
	}

	var info IPInfo
	info.IP = ipStr

	// 1. If custom ASNResolver is registered, use it directly without remote API calls
	if ASNResolver != nil {
		info.ASN = ASNResolver(ipStr)
		if CityResolver != nil {
			info.City = CityResolver(ipStr)
		}
		ipInfoCache.Store(ipStr, info)
		return info
	}

	if CityResolver != nil {
		info.City = CityResolver(ipStr)
	}

	// 2. Dynamic lookup via IP API if ASN is not resolved
	if info.ASN == "" {
		reqURL := fmt.Sprintf("http://ip-api.com/json/%s?fields=status,as,isp,city,country", ipStr)
		req, err := http.NewRequest(http.MethodGet, reqURL, nil)
		if err == nil {
			req.Header.Set("User-Agent", "Polaris-IPResolver/1.0")
			resp, err := ipHTTPClient.Do(req)
			if err == nil && resp.StatusCode == http.StatusOK {
				var data struct {
					Status  string `json:"status"`
					AS      string `json:"as"`
					ISP     string `json:"isp"`
					City    string `json:"city"`
					Country string `json:"country"`
				}
				if json.NewDecoder(resp.Body).Decode(&data) == nil && data.Status == "success" {
					info.ISP = data.ISP
					if info.City == "" {
						info.City = data.City
					}
					info.Country = data.Country
					info.ASName = data.AS

					if match := reASN.FindString(data.AS); match != "" {
						info.ASN = strings.ToUpper(match)
					}

					// Special carrier heuristic fallback when AS field is blank (e.g. internal CN2 backbone)
					if info.ASN == "" {
						lowerISP := strings.ToLower(data.ISP)
						if strings.Contains(lowerISP, "next carrying network") || strings.Contains(lowerISP, "cn2") {
							info.ASN = "AS4809"
						} else if strings.Contains(lowerISP, "chinanet") {
							info.ASN = "AS4134"
						} else if strings.Contains(lowerISP, "cuii") || strings.Contains(lowerISP, "cncnet") {
							info.ASN = "AS9929"
						}
					}
				}
				_ = resp.Body.Close()
			}
		}
	}

	ipInfoCache.Store(ipStr, info)
	return info
}

// IPv4ToASN maps an IP address or routing hop string to its authoritative ASN.
// No IP prefixes are hardcoded; resolution is performed dynamically.
func IPv4ToASN(ip string) string {
	raw := strings.TrimSpace(ip)
	if raw == "" {
		return ""
	}

	// 1. Direct ASN format or token in traceroute output (e.g. "AS4809", "AS9929")
	if match := reASN.FindString(raw); match != "" {
		return strings.ToUpper(match)
	}

	// 2. Check carrier identity keywords in reverse DNS or text
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "cuii") || strings.Contains(lower, "9929"):
		return "AS9929"
	case strings.Contains(lower, "cug") || strings.Contains(lower, "10099"):
		return "AS10099"
	case strings.Contains(lower, "cn2"):
		return "AS4809"
	case strings.Contains(lower, "ctgnet") || strings.Contains(lower, "ctg"):
		return "AS23764"
	case strings.Contains(lower, "cmin2") || strings.Contains(lower, "58807"):
		return "AS58807"
	case strings.Contains(lower, "cmi") || strings.Contains(lower, "58453"):
		return "AS58453"
	case strings.Contains(lower, "chinanet") || strings.Contains(lower, "4134"):
		return "AS4134"
	case strings.Contains(lower, "china169") || strings.Contains(lower, "4837"):
		return "AS4837"
	case strings.Contains(lower, "cmnet") || strings.Contains(lower, "9808"):
		return "AS9808"
	}

	// 3. Dynamic lookup via registered hook or IP database
	if ASNResolver != nil {
		if a := ASNResolver(raw); a != "" {
			return a
		}
	}

	info := LookupIPInfo(raw)
	if info.ASN != "" {
		return info.ASN
	}

	return ""
}

// IPv4ToCity extracts the geographic city or international region for intermediate carrier hops.
// Resolved dynamically from the IP database; no hardcoded city abbreviations.
func IPv4ToCity(ip string) string {
	raw := strings.TrimSpace(ip)
	if raw == "" {
		return ""
	}

	// 1. Dynamic IP resolution via authoritative database (e.g. ip2region)
	if CityResolver != nil {
		if c := CityResolver(raw); c != "" {
			return c
		}
	}

	// 2. Query dynamic IP info cache
	info := LookupIPInfo(raw)
	if info.City != "" {
		return info.City
	}

	return ""
}

// IsDomesticIP checks whether an IP address is located in Mainland China (excluding HK, MO, TW).
func IsDomesticIP(ip string) bool {
	raw := strings.TrimSpace(ip)
	if raw == "" {
		return false
	}
	if IsDomesticResolver != nil {
		return IsDomesticResolver(raw)
	}
	info := LookupIPInfo(raw)
	if info.Country == "CN" || info.Country == "China" {
		return true
	}
	asn := info.ASN
	if asn == "AS4134" || asn == "AS4837" || asn == "AS9808" || asn == "AS9929" || asn == "AS58807" {
		return true
	}
	return false
}

// ASNToNameWithLocation converts an authoritative ASN and detected city to a readable trajectory node label.
func ASNToNameWithLocation(asn, city string) string {
	baseName, ok := carrierNames[asn]
	if !ok {
		if asn == "" {
			if city != "" {
				return fmt.Sprintf("节点 (%s)", city)
			}
			return ""
		}
		if city != "" {
			return fmt.Sprintf("%s (%s)", asn, city)
		}
		return asn
	}

	if city != "" {
		return fmt.Sprintf("%s (%s·%s)", asn, baseName, city)
	}
	return fmt.Sprintf("%s (%s)", asn, baseName)
}

// ASNToName converts an authoritative ASN identifier to a readable trajectory node label.
func ASNToName(asn string) string {
	return ASNToNameWithLocation(asn, "")
}

// AnalyzeTelecomRoute evaluates intermediate IP hops to identify China Telecom route quality.
// Quality is classified based on BGP ASNs and carrier path metadata, not IP prefix rules.
func AnalyzeTelecomRoute(hops []string, latencyMs int) string {
	if len(hops) > 0 {
		var asns []string
		cn2Index, ct163Index, ctgIndex := -1, -1, -1
		cu9929Index, cmin2Index := -1, -1
		cn2Count, ct163Count := 0, 0

		for idx, ip := range hops {
			asn := IPv4ToASN(ip)
			if asn != "" {
				asns = append(asns, asn)
				if asn == "AS4809" {
					if cn2Index < 0 {
						cn2Index = idx
					}
					cn2Count++
				} else if asn == "AS4134" {
					if ct163Index < 0 {
						ct163Index = idx
					}
					ct163Count++
				} else if asn == "AS23764" && ctgIndex < 0 {
					ctgIndex = idx
				} else if (asn == "AS9929" || asn == "AS10099") && cu9929Index < 0 {
					cu9929Index = idx
				} else if asn == "AS58807" && cmin2Index < 0 {
					cmin2Index = idx
				}
			}
		}

		if cn2Index >= 0 {
			// If CN2 appears with 163 before it, it's CN2 GT (半程CN2)
			if ct163Index >= 0 && ct163Index < cn2Index {
				return "电信 CN2 GT"
			}
			// If followed by multiple 163 backbone hops, it's CN2 GT
			if ct163Index > cn2Index && ct163Count > 1 {
				return "电信 CN2 GT"
			}
			// Otherwise confirmed CN2 GIA (全程精品线路)
			return "电信 CN2 GIA"
		}

		if ctgIndex >= 0 {
			return "电信 CTGNet"
		}

		// 跨网借道 联通 AS9929 / AS10099 精品网回程
		if cu9929Index >= 0 {
			return "电信 9929"
		}

		// 跨网借道 移动 AS58807 CMIN2 精品网回程
		if cmin2Index >= 0 {
			return "电信 CMIN2"
		}

		if ct163Index >= 0 || ct163Count > 0 {
			return "电信 163 骨干"
		}
	}

	if latencyMs > 0 {
		if latencyMs < 60 {
			return "电信 BGP 直连"
		}
		return "电信 163 骨干"
	}
	return "电信超时"
}

// AnalyzeUnicomRoute evaluates intermediate IP hops to identify China Unicom route quality.
// Quality is classified based on BGP ASNs and carrier path metadata, not IP prefix rules.
func AnalyzeUnicomRoute(hops []string, latencyMs int) string {
	if len(hops) > 0 {
		has9929 := false
		has10099 := false
		has4837 := false
		hasCN2 := false
		hasCMIN2 := false

		for _, ip := range hops {
			asn := IPv4ToASN(ip)
			switch asn {
			case "AS9929":
				has9929 = true
			case "AS10099":
				has10099 = true
			case "AS4837":
				has4837 = true
			case "AS4809":
				hasCN2 = true
			case "AS58807":
				hasCMIN2 = true
			}
		}

		// AS9929 (联通A网 / CUII) 具备最高优先级
		if has9929 {
			return "联通 9929"
		}

		// AS10099 (CUG VIP / China Unicom Global 精品出海段) 归入 9929 精品网体系
		if has10099 {
			return "联通 9929"
		}

		// 跨网 CN2 优质回程
		if hasCN2 {
			return "联通 CN2 GIA"
		}

		// 跨网 CMIN2 优质回程
		if hasCMIN2 {
			return "联通 CMIN2"
		}

		// AS4837 联通 169 普通骨干
		if has4837 {
			return "联通 4837"
		}
	}

	if latencyMs > 0 {
		if latencyMs < 60 {
			return "联通 BGP 直连"
		}
		return "联通 4837"
	}
	return "联通超时"
}

// AnalyzeMobileRoute evaluates intermediate IP hops to identify China Mobile route quality.
// Quality is classified based on BGP ASNs and carrier path metadata, not IP prefix rules.
func AnalyzeMobileRoute(hops []string, latencyMs int) string {
	if len(hops) > 0 {
		hasCMIN2 := false
		hasCN2 := false
		has9929 := false
		hasCMI := false

		for _, ip := range hops {
			asn := IPv4ToASN(ip)
			switch asn {
			case "AS58807":
				hasCMIN2 = true
			case "AS4809":
				hasCN2 = true
			case "AS9929", "AS10099":
				has9929 = true
			case "AS58453", "AS9808":
				hasCMI = true
			}
		}

		if hasCMIN2 {
			return "移动 CMIN2"
		}

		if hasCN2 {
			return "移动 CN2 GIA"
		}

		if has9929 {
			return "移动 9929"
		}

		if hasCMI {
			return "移动 CMI"
		}
	}

	if latencyMs > 0 {
		if latencyMs < 60 {
			return "移动 BGP 直连"
		}
		return "移动 CMI"
	}
	return "移动超时"
}

// BuildTracePath constructs a clean hop trajectory from real traceroute intermediate hops, including geographical regions.
func BuildTracePath(hops []string, targetName string) string {
	type asnNode struct {
		asn  string
		city string
	}
	var nodes []asnNode
	originCity := ""

	for i, ip := range hops {
		asn := IPv4ToASN(ip)
		city := IPv4ToCity(ip)

		if i < 3 && originCity == "" && city != "" {
			originCity = city
		}

		if asn == "" {
			continue
		}

		foundIdx := -1
		for idx, n := range nodes {
			if n.asn == asn {
				foundIdx = idx
				break
			}
		}

		if foundIdx >= 0 {
			if nodes[foundIdx].city == "" && city != "" {
				nodes[foundIdx].city = city
			}
		} else {
			nodes = append(nodes, asnNode{asn: asn, city: city})
		}
	}

	if len(nodes) == 0 {
		return ""
	}

	var seq []string
	for _, n := range nodes {
		name := ASNToNameWithLocation(n.asn, n.city)
		if name != "" {
			seq = append(seq, name)
		}
	}

	if len(seq) == 0 {
		return ""
	}

	// If origin city is identified and the first node's city is empty, enrich it
	if len(nodes) > 0 && nodes[0].city == "" && originCity != "" {
		seq[0] = ASNToNameWithLocation(nodes[0].asn, originCity)
	}

	if targetName != "" {
		seq = append(seq, targetName)
	}
	return strings.Join(seq, " ➔ ")
}

// BuildTracePathFallback returns the genuine endpoint trajectory without fabricating unverified intermediate hops.
func BuildTracePathFallback(route, targetName string) string {
	if targetName == "" {
		return ""
	}
	return fmt.Sprintf("节点 ➔ %s (骨干跃点未回显)", targetName)
}

// BuildRealInboundTracePath constructs a genuine inbound trajectory from real probe source metadata
// (city, ASN, carrier) to the destination node. When real intermediate hops are provided, it dynamically evaluates
// every IP hop via BuildTracePath; when hops are not available, it strictly connects the verified probe origin
// with the destination node, never fabricating intermediate hops.
func BuildRealInboundTracePath(probeCity string, probeASN int, carrier, destName string, hops []string) string {
	targetLabel := destName
	if targetLabel == "" {
		targetLabel = "目标节点"
	}

	// 1. If intermediate hops exist, dynamically parse them based on real IPs
	if len(hops) > 0 {
		if path := BuildTracePath(hops, targetLabel); path != "" {
			return path
		}
	}

	// 2. Build genuine source label strictly from verified probe metadata
	srcLabel := ""
	if probeASN > 0 {
		asnStr := fmt.Sprintf("AS%d", probeASN)
		srcLabel = ASNToNameWithLocation(asnStr, probeCity)
		if srcLabel == "" {
			if probeCity != "" {
				srcLabel = fmt.Sprintf("AS%d (%s·%s)", probeASN, carrier, probeCity)
			} else {
				srcLabel = fmt.Sprintf("AS%d (%s)", probeASN, carrier)
			}
		}
	} else if probeCity != "" {
		srcLabel = fmt.Sprintf("%s%s探针", probeCity, carrier)
	} else if carrier != "" {
		srcLabel = fmt.Sprintf("%s探针", carrier)
	}

	if srcLabel == "" {
		return targetLabel
	}
	return fmt.Sprintf("%s ➔ %s", srcLabel, targetLabel)
}

// BuildInboundTracePath constructs an inbound trajectory from probe source to the target node.
// It strictly respects genuine endpoints and never fabricates unverified intermediate hops.
func BuildInboundTracePath(route, carrier, destName string) string {
	targetLabel := "目标节点"
	if destName != "" && destName != "目标机房" && destName != "服务器节点" {
		targetLabel = destName
	}
	srcLabel := carrier
	if srcLabel == "" {
		srcLabel = "探针"
	}
	return fmt.Sprintf("%s ➔ %s", srcLabel, targetLabel)
}
