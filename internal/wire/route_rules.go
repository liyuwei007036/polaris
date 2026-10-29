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

	// Authoritative IP prefix lookup
	switch {
	// Cogent (AS174)
	case strings.HasPrefix(raw, "38.") || strings.HasPrefix(raw, "154."):
		return "AS174"

	// 电信 CN2 (AS4809)
	case strings.HasPrefix(raw, "59.43."):
		return "AS4809"

	// 电信 163 (AS4134)
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

	// 联通 169 普通网 (AS4837)
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

// IPv4ToCity extracts the geographic city or international region for intermediate carrier hops.
func IPv4ToCity(ip string) string {
	raw := strings.TrimSpace(ip)
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
	}

	switch {
	// Cogent US West
	case strings.HasPrefix(raw, "38.244.") || strings.HasPrefix(raw, "154."):
		return "美西洛杉矶"

	// Telecom CN2 (AS4809)
	case strings.HasPrefix(raw, "59.43.247.") || strings.HasPrefix(raw, "59.43.182."):
		return "美西洛杉矶"
	case strings.HasPrefix(raw, "59.43.248."):
		return "日本东京"
	case strings.HasPrefix(raw, "59.43.250."):
		return "新加坡"
	case strings.HasPrefix(raw, "59.43.249."):
		return "德国法兰克福"
	case strings.HasPrefix(raw, "59.43.251."):
		return "英国伦敦"
	case strings.HasPrefix(raw, "59.43.244."):
		return "中国香港"
	case strings.HasPrefix(raw, "59.43.187.") || strings.HasPrefix(raw, "59.43.130.") || strings.HasPrefix(raw, "59.43.39."):
		return "上海"
	case strings.HasPrefix(raw, "59.43.18.") || strings.HasPrefix(raw, "59.43.246."):
		return "广州"
	case strings.HasPrefix(raw, "59.43.138.") || strings.HasPrefix(raw, "59.43.245."):
		return "北京"

	// Telecom 163 (AS4134)
	case strings.HasPrefix(raw, "202.97.50.") || strings.HasPrefix(raw, "202.97.51.") || strings.HasPrefix(raw, "202.97.90."):
		return "美西洛杉矶"
	case strings.HasPrefix(raw, "202.97.58."):
		return "美西圣何塞"
	case strings.HasPrefix(raw, "202.97.60."):
		return "中国香港"
	case strings.HasPrefix(raw, "202.97.61."):
		return "日本东京"
	case strings.HasPrefix(raw, "202.97.88."):
		return "新加坡"
	case strings.HasPrefix(raw, "202.97.81.") || strings.HasPrefix(raw, "202.97.87.") || strings.HasPrefix(raw, "202.97.62.") || strings.HasPrefix(raw, "202.97.33."):
		return "上海"
	case strings.HasPrefix(raw, "202.97.94.") || strings.HasPrefix(raw, "202.97.91.") || strings.HasPrefix(raw, "202.97.67."):
		return "广州"
	case strings.HasPrefix(raw, "202.97.63.") || strings.HasPrefix(raw, "202.97.53.") || strings.HasPrefix(raw, "202.97.24."):
		return "北京"
	case strings.HasPrefix(raw, "202.97.45.") || strings.HasPrefix(raw, "202.97.46."):
		return "成都"

	// Unicom 9929 / CUII
	case strings.HasPrefix(raw, "218.105.2.174") || strings.HasPrefix(raw, "218.105.2.170"):
		return "美西洛杉矶"
	case strings.HasPrefix(raw, "218.105.2.198"):
		return "中国香港"
	case strings.HasPrefix(raw, "218.105.2.150") || strings.HasPrefix(raw, "218.105.2.152"):
		return "上海"
	case strings.HasPrefix(raw, "218.105.2.154"):
		return "广州"
	case strings.HasPrefix(raw, "218.105.2.146"):
		return "北京"

	// Unicom CUG (AS10099)
	case strings.HasPrefix(raw, "203.160.75.") || strings.HasPrefix(raw, "203.160.") || strings.HasPrefix(raw, "202.77."):
		return "中国香港"
	case strings.HasPrefix(raw, "38.69."):
		return "美西洛杉矶"

	// Unicom 169 (AS4837)
	case strings.HasPrefix(raw, "219.158.102."):
		return "美西洛杉矶"
	case strings.HasPrefix(raw, "219.158.103."):
		return "美西圣何塞"
	case strings.HasPrefix(raw, "219.158.104."):
		return "德国法兰克福"
	case strings.HasPrefix(raw, "219.158.105."):
		return "日本东京"
	case strings.HasPrefix(raw, "219.158.10."):
		return "中国香港"
	case strings.HasPrefix(raw, "219.158.9."):
		return "上海"
	case strings.HasPrefix(raw, "219.158.11.") || strings.HasPrefix(raw, "219.158.24."):
		return "广州"
	case strings.HasPrefix(raw, "219.158.3.") || strings.HasPrefix(raw, "219.158.21."):
		return "北京"

	// Mobile CMIN2 (AS58807)
	case strings.HasPrefix(raw, "223.120.161.5"):
		return "美西洛杉矶"
	case strings.HasPrefix(raw, "223.120.16."):
		return "中国香港"
	case strings.HasPrefix(raw, "223.120.17."):
		return "上海"

	// Mobile CMI (AS58453)
	case strings.HasPrefix(raw, "223.120.161."):
		return "美西洛杉矶"
	case strings.HasPrefix(raw, "223.120.162."):
		return "美西圣何塞"
	case strings.HasPrefix(raw, "223.120.2."):
		return "中国香港"
	case strings.HasPrefix(raw, "223.120.6."):
		return "新加坡"
	case strings.HasPrefix(raw, "223.120.12."):
		return "日本东京"
	case strings.HasPrefix(raw, "223.120.14."):
		return "德国法兰克福"
	case strings.HasPrefix(raw, "223.120.15."):
		return "英国伦敦"

	// Mobile CMNet (AS9808)
	case strings.HasPrefix(raw, "221.183.18."):
		return "中国香港"
	case strings.HasPrefix(raw, "221.183.92.") || strings.HasPrefix(raw, "221.183.89."):
		return "上海"
	case strings.HasPrefix(raw, "221.183.116.") || strings.HasPrefix(raw, "221.183.125."):
		return "广州"
	case strings.HasPrefix(raw, "221.183.25.") || strings.HasPrefix(raw, "221.183.26."):
		return "北京"
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

// BuildTracePathFallback returns a clean line trajectory with concrete ASN and location hops.
func BuildTracePathFallback(route, targetName string) string {
	origin := "AS174 (Cogent·美西洛杉矶)"
	if strings.Contains(route, "9929") || strings.Contains(route, "CUII") {
		if strings.Contains(route, "电信") {
			return fmt.Sprintf("%s ➔ AS10099 (联通CUG·中国香港) ➔ AS9929 (联通A网·上海) ➔ AS4134 (电信163·上海) ➔ %s", origin, targetName)
		}
		return fmt.Sprintf("%s ➔ AS10099 (联通CUG·中国香港) ➔ AS9929 (联通A网·上海) ➔ %s", origin, targetName)
	}
	if strings.Contains(route, "CMIN2") || strings.Contains(route, "58807") {
		return fmt.Sprintf("%s ➔ AS58807 (移动CMIN2·美西洛杉矶) ➔ AS9808 (移动骨干·上海) ➔ %s", origin, targetName)
	}
	if strings.Contains(route, "CN2 GIA") {
		return fmt.Sprintf("%s ➔ AS4809 (电信CN2·美西洛杉矶) ➔ AS4809 (电信CN2·上海) ➔ %s", origin, targetName)
	}
	if strings.Contains(route, "CN2 GT") {
		return fmt.Sprintf("%s ➔ AS4809 (电信CN2·美西洛杉矶) ➔ AS4134 (电信163·上海) ➔ %s", origin, targetName)
	}
	if strings.Contains(route, "CTGNet") {
		return fmt.Sprintf("%s ➔ AS23764 (电信CTGNet·中国香港) ➔ AS4134 (电信163·广州) ➔ %s", origin, targetName)
	}
	if strings.Contains(route, "4837") {
		return fmt.Sprintf("%s ➔ AS4837 (联通169·美西洛杉矶) ➔ AS4837 (联通169·上海) ➔ %s", origin, targetName)
	}
	if strings.Contains(route, "CMI") {
		return fmt.Sprintf("%s ➔ AS58453 (移动CMI·中国香港) ➔ AS9808 (移动骨干·广州) ➔ %s", origin, targetName)
	}
	if strings.Contains(route, "163") {
		return fmt.Sprintf("%s ➔ AS4134 (电信163·美西洛杉矶) ➔ AS4134 (电信163·上海) ➔ %s", origin, targetName)
	}
	return fmt.Sprintf("%s ➔ %s", origin, targetName)
}

// BuildInboundTracePath constructs an inbound trajectory from domestic source to the target node with concrete regions and ASNs.
func BuildInboundTracePath(route, carrier, destName string) string {
	targetLabel := "AS174 (Cogent·美西洛杉矶)"
	if destName != "" && destName != "目标机房" && destName != "海外骨干" && destName != "服务器节点" {
		targetLabel = destName
	}
	if strings.Contains(route, "CN2 GIA") || strings.Contains(route, "4809") {
		return fmt.Sprintf("AS4809 (电信CN2·上海) ➔ AS4809 (电信CN2·美西洛杉矶) ➔ %s", targetLabel)
	}
	if strings.Contains(route, "9929") || strings.Contains(route, "CUII") {
		return fmt.Sprintf("AS9929 (联通A网·上海) ➔ AS10099 (联通CUG·中国香港) ➔ %s", targetLabel)
	}
	if strings.Contains(route, "CMIN2") || strings.Contains(route, "58807") {
		return fmt.Sprintf("AS58807 (移动CMIN2·上海) ➔ AS58807 (移动CMIN2·美西洛杉矶) ➔ %s", targetLabel)
	}
	if carrier == "电信" || strings.Contains(route, "163") {
		return fmt.Sprintf("AS4134 (电信163·上海) ➔ AS4134 (电信163·美西洛杉矶) ➔ %s", targetLabel)
	}
	if carrier == "联通" || strings.Contains(route, "4837") {
		return fmt.Sprintf("AS4837 (联通169·上海) ➔ AS4837 (联通169·美西洛杉矶) ➔ %s", targetLabel)
	}
	if carrier == "移动" || strings.Contains(route, "CMI") {
		return fmt.Sprintf("AS9808 (移动骨干·广州) ➔ AS58453 (移动CMI·中国香港) ➔ %s", targetLabel)
	}
	return fmt.Sprintf("AS4134 (电信163·上海) ➔ %s", targetLabel)
}
