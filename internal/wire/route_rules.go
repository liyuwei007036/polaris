package wire

import (
	"fmt"
	"strconv"
	"strings"
)

// HasHopPrefix checks if any hop in the list starts with the given prefix.
func HasHopPrefix(hops []string, prefix string) bool {
	for _, ip := range hops {
		if strings.HasPrefix(ip, prefix) {
			return true
		}
	}
	return false
}

// HasCN2Hop checks if any hop belongs to China Telecom CN2 (AS4809: 59.43.*).
func HasCN2Hop(hops []string) bool {
	return HasHopPrefix(hops, "59.43.")
}

// isCMIN2IP checks if an IPv4 address belongs to China Mobile CMIN2 (AS58807),
// aligned with the authoritative prefix registry of oneclickvirt/backtrace.
func isCMIN2IP(ipStr string) bool {
	if !strings.HasPrefix(ipStr, "223.") {
		return false
	}
	parts := strings.Split(strings.TrimSpace(ipStr), ".")
	if len(parts) < 3 {
		return false
	}
	p1, err1 := strconv.Atoi(parts[1])
	p2, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil {
		return false
	}

	if p1 == 118 && p2 == 32 {
		return true
	}
	if p1 == 120 && ((p2 >= 16 && p2 <= 19) || p2 >= 128) {
		return true
	}
	if p1 == 119 {
		switch p2 {
		case 8, 9, 74, 75, 88, 89, 100, 252, 253:
			return true
		default:
			if (p2 >= 10 && p2 <= 15) || (p2 >= 26 && p2 <= 29) || (p2 >= 32 && p2 <= 37) {
				return true
			}
		}
	}
	return false
}

// IPv4ToASN maps an IP address or routing hop string to its authoritative China carrier ASN.
// Implements the industry-standard rules from oneclickvirt/backtrace and zhanghanyun/backtrace.
func IPv4ToASN(ip string) string {
	raw := strings.TrimSpace(ip)
	lower := strings.ToLower(raw)

	// Explicit text / token match (from reverse DNS or traceroute output)
	if strings.Contains(lower, "as9929") || strings.Contains(lower, "9929") || strings.Contains(lower, "cuii") {
		return "AS9929"
	}
	if strings.Contains(lower, "as10099") || strings.Contains(lower, "10099") || strings.Contains(lower, "cug") {
		return "AS10099"
	}
	if strings.Contains(lower, "as4809") || strings.Contains(lower, "cn2") {
		return "AS4809"
	}
	if strings.Contains(lower, "as58807") || strings.Contains(lower, "cmin2") {
		return "AS58807"
	}
	if strings.Contains(lower, "as23764") || strings.Contains(lower, "ctgnet") || strings.Contains(lower, "ctg") {
		return "AS23764"
	}
	if strings.Contains(lower, "as4134") || strings.Contains(lower, "chinanet") {
		return "AS4134"
	}
	if strings.Contains(lower, "as4837") || strings.Contains(lower, "china169") {
		return "AS4837"
	}
	if strings.Contains(lower, "as58453") {
		return "AS58453"
	}
	if strings.Contains(lower, "as9808") || strings.Contains(lower, "cmnet") {
		return "AS9808"
	}
	if strings.Contains(lower, "as174") || strings.Contains(lower, "cogent") {
		return "AS174"
	}
	if strings.Contains(lower, "as45090") || strings.Contains(lower, "tencent") {
		return "AS45090"
	}
	if strings.Contains(lower, "as37963") || strings.Contains(lower, "alibaba") || strings.Contains(lower, "aliyun") {
		return "AS37963"
	}
	if strings.Contains(lower, "as13335") || strings.Contains(lower, "cloudflare") {
		return "AS13335"
	}
	if strings.Contains(lower, "as16509") || strings.Contains(lower, "amazon") || strings.Contains(lower, "aws") {
		return "AS16509"
	}
	if strings.Contains(lower, "as24940") || strings.Contains(lower, "hetzner") {
		return "AS24940"
	}

	// Authoritative IP prefix lookup (Core Carrier BGP Backbones only)
	switch {
	// Cogent (AS174 - ARIN 38.0.0.0/8)
	case strings.HasPrefix(raw, "38."):
		return "AS174"

	// 电信 CN2 (AS4809)
	case strings.HasPrefix(raw, "59.43."):
		return "AS4809"

	// 电信 163 骨干 (AS4134)
	case strings.HasPrefix(raw, "202.97."):
		return "AS4134"

	// 电信 CTGNet (AS23764)
	case strings.HasPrefix(raw, "69.194.") || strings.HasPrefix(raw, "203.22.") || strings.HasPrefix(raw, "203.208."):
		return "AS23764"

	// 联通精品网 / A网 / CUII (AS9929)
	case strings.HasPrefix(raw, "218.105.") || strings.HasPrefix(raw, "210.51.") ||
		strings.HasPrefix(raw, "210.52.") || strings.HasPrefix(raw, "210.53.") ||
		strings.HasPrefix(raw, "218.107.") || strings.HasPrefix(raw, "218.104.") ||
		strings.HasPrefix(raw, "218.106.") || strings.HasPrefix(raw, "210.13.") ||
		strings.HasPrefix(raw, "221.12."):
		return "AS9929"

	// 联通国际 CUG / 跨境汇聚 (AS10099)
	case strings.HasPrefix(raw, "202.77.") || strings.HasPrefix(raw, "43.252.") || strings.HasPrefix(raw, "61.14.") ||
		strings.HasPrefix(raw, "103.107.") || strings.HasPrefix(raw, "43.251.") ||
		strings.HasPrefix(raw, "38.69.") || strings.HasPrefix(raw, "203.160.") ||
		strings.HasPrefix(raw, "210.209."):
		return "AS10099"

	// 联通 169 普通骨干 (AS4837)
	case strings.HasPrefix(raw, "219.158.") || strings.HasPrefix(raw, "221.192.") || strings.HasPrefix(raw, "221.194."):
		return "AS4837"

	// 移动 CMIN2 精品网 (AS58807)
	case isCMIN2IP(raw):
		return "AS58807"

	// 移动 CMI 国际出口 (AS58453)
	case strings.HasPrefix(raw, "223.118.") || strings.HasPrefix(raw, "223.119.") ||
		strings.HasPrefix(raw, "223.120.") || strings.HasPrefix(raw, "223.121."):
		return "AS58453"

	// 移动 CMNET 骨干网 (AS9808)
	case strings.HasPrefix(raw, "221.183.") || strings.HasPrefix(raw, "111.24."):
		return "AS9808"

	default:
		return ""
	}
}

// AnalyzeTelecomRoute evaluates intermediate IP hops to identify China Telecom route quality.
// Conservative standard aligned with backtrace / nexttrace.
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
			// If followed by multiple 163 backbone hops, it's CN2 GT / CN2 Mixed
			if ct163Index > cn2Index && ct163Count > 1 {
				return "电信 CN2 GT"
			}
			// Otherwise confirmed CN2 GIA (全程CN2精品线路)
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
// Aligned with backtrace: AS9929 (CUII/A网) and AS10099 (CUG) identify 联通 9929 精品线路.
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
// Aligned with backtrace: AS58807 is CMIN2精品线路, AS58453/AS9808 is CMI.
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

// CityResolver is a dynamic hook (set by the control plane using ip2region or remote IP database)
// to resolve IP addresses to their true geographic city/region without relying on hardcoded IP prefixes.
var CityResolver func(ip string) string

// IPv4ToCity extracts the geographic city or international region for intermediate carrier hops.
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

	// 2. Carrier router rDNS IATA city codes (e.g. lax-xx, can-xx, sha-xx)
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "lax") || strings.Contains(lower, "losangeles"):
		return "洛杉矶"
	case strings.Contains(lower, "sjc") || strings.Contains(lower, "sanjose"):
		return "圣何塞"
	case strings.Contains(lower, "fra") || strings.Contains(lower, "frankfurt"):
		return "德国法兰克福"
	case strings.Contains(lower, "lon") || strings.Contains(lower, "london"):
		return "英国伦敦"
	case strings.Contains(lower, "tyo") || strings.Contains(lower, "tokyo"):
		return "日本东京"
	case strings.Contains(lower, "sin") || strings.Contains(lower, "singapore"):
		return "新加坡"
	case strings.Contains(lower, "hkg") || strings.Contains(lower, "hongkong"):
		return "中国香港"
	case strings.Contains(lower, "sha") || strings.Contains(lower, "shanghai"):
		return "上海"
	case strings.Contains(lower, "can") || strings.Contains(lower, "guangzhou"):
		return "广州"
	case strings.Contains(lower, "pek") || strings.Contains(lower, "beijing"):
		return "北京"
	case strings.Contains(lower, "nkg") || strings.Contains(lower, "nanjing"):
		return "南京"
	case strings.Contains(lower, "ctu") || strings.Contains(lower, "chengdu"):
		return "成都"
	case strings.Contains(lower, "wuh") || strings.Contains(lower, "wuhan"):
		return "武汉"
	case strings.Contains(lower, "szx") || strings.Contains(lower, "shenzhen"):
		return "深圳"
	}

	return ""
}

// ASNToNameWithLocation converts an authoritative ASN and detected city to a readable trajectory node label.
func ASNToNameWithLocation(asn, city string) string {
	base := ""
	switch asn {
	case "AS4809":
		base = "AS4809 (电信CN2"
	case "AS4134":
		base = "AS4134 (电信163"
	case "AS23764":
		base = "AS23764 (电信CTGNet"
	case "AS9929":
		base = "AS9929 (联通A网"
	case "AS10099":
		base = "AS10099 (联通CUG"
	case "AS4837":
		base = "AS4837 (联通169"
	case "AS58807":
		base = "AS58807 (移动CMIN2"
	case "AS58453":
		base = "AS58453 (移动CMI"
	case "AS9808":
		base = "AS9808 (移动骨干"
	case "AS174":
		base = "AS174 (Cogent"
	case "AS45090":
		base = "AS45090 (腾讯云"
	case "AS37963":
		base = "AS37963 (阿里云"
	case "AS13335":
		base = "AS13335 (Cloudflare"
	case "AS16509":
		base = "AS16509 (AWS"
	case "AS24940":
		base = "AS24940 (Hetzner"
	case "AS8075":
		base = "AS8075 (Azure"
	case "AS2914":
		base = "AS2914 (NTT"
	case "AS1299":
		base = "AS1299 (Arelion"
	case "AS6939":
		base = "AS6939 (HE"
	default:
		return ""
	}

	if city != "" {
		return base + "·" + city + ")"
	}
	return base + ")"
}

// ASNToName converts an authoritative ASN identifier to a readable trajectory node label.
func ASNToName(asn string) string {
	return ASNToNameWithLocation(asn, "")
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

// BuildRealInboundTracePath constructs a genuine inbound trajectory from real domestic probe source metadata
// (city, ASN, carrier) to the destination node. When real intermediate hops are provided, it dynamically evaluates
// every IP hop via BuildTracePath; when hops are not available, it strictly connects the verified probe origin
// with the destination node, never fabricating intermediate hops.
func BuildRealInboundTracePath(probeCity string, probeASN int, carrier, destName string, hops []string) string {
	targetLabel := destName
	if targetLabel == "" {
		targetLabel = "境外节点"
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
		srcLabel = fmt.Sprintf("国内%s探针", carrier)
	}

	if srcLabel == "" {
		return targetLabel
	}
	return fmt.Sprintf("%s ➔ %s", srcLabel, targetLabel)
}

// BuildInboundTracePath constructs an inbound trajectory from domestic source to the target node.
// It strictly respects genuine endpoints and never fabricates unverified intermediate hops.
func BuildInboundTracePath(route, carrier, destName string) string {
	targetLabel := "境外节点"
	if destName != "" && destName != "目标机房" && destName != "海外骨干" && destName != "服务器节点" {
		targetLabel = destName
	}
	srcLabel := carrier
	if srcLabel == "" {
		srcLabel = "国内探针"
	}
	return fmt.Sprintf("%s ➔ %s", srcLabel, targetLabel)
}
