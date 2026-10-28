package wire

import (
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

	// Authoritative IP prefix lookup
	switch {
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

		if ct163Index >= 0 || ct163Count > 0 {
			return "电信 163 骨干"
		}
	}

	if latencyMs > 0 {
		if latencyMs < 80 {
			return "电信 BGP 直连"
		}
		return "电信普通直连"
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
			return "CN2 GIA"
		}

		// AS4837 联通 169 普通骨干
		if has4837 {
			return "联通 4837"
		}
	}

	if latencyMs > 0 {
		if latencyMs < 80 {
			return "联通 BGP 直连"
		}
		return "联通普通直连"
	}
	return "联通超时"
}

// AnalyzeMobileRoute evaluates intermediate IP hops to identify China Mobile route quality.
// Aligned with backtrace: AS58807 is CMIN2精品线路, AS58453/AS9808 is CMI.
func AnalyzeMobileRoute(hops []string, latencyMs int) string {
	if len(hops) > 0 {
		hasCMIN2 := false
		hasCN2 := false
		hasCMI := false

		for _, ip := range hops {
			asn := IPv4ToASN(ip)
			switch asn {
			case "AS58807":
				hasCMIN2 = true
			case "AS4809":
				hasCN2 = true
			case "AS58453", "AS9808":
				hasCMI = true
			}
		}

		if hasCMIN2 {
			return "移动 CMIN2"
		}

		if hasCN2 {
			return "CN2 GIA"
		}

		if hasCMI {
			return "移动 CMI"
		}
	}

	if latencyMs > 0 {
		if latencyMs < 80 {
			return "移动 BGP 直连"
		}
		return "移动普通直连"
	}
	return "移动超时"
}
