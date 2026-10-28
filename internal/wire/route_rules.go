package wire

import "strings"

func HasHopPrefix(hops []string, prefix string) bool {
	for _, ip := range hops {
		if strings.HasPrefix(ip, prefix) {
			return true
		}
	}
	return false
}

func HasCN2Hop(hops []string) bool {
	return HasHopPrefix(hops, "59.43.")
}

// AnalyzeTelecomRoute evaluates intermediate IP hops to identify China Telecom route quality.
func AnalyzeTelecomRoute(hops []string, latencyMs int) string {
	if len(hops) > 0 {
		hasCN2 := HasCN2Hop(hops)
		has163 := HasHopPrefix(hops, "202.97.")
		if hasCN2 && !has163 {
			return "电信 CN2 GIA"
		}
		if hasCN2 && has163 {
			return "电信 CN2 GT"
		}
		if has163 {
			return "电信 163 骨干"
		}
		if HasHopPrefix(hops, "203.208.") || HasHopPrefix(hops, "202.97.12.") {
			return "电信 CTGNet"
		}
	}
	if latencyMs > 0 {
		if latencyMs < 80 {
			return "电信 BGP 直连"
		}
		return "电信普通直连"
	}
	return "电信直连"
}

// AnalyzeUnicomRoute evaluates intermediate IP hops to identify China Unicom route quality.
func AnalyzeUnicomRoute(hops []string, latencyMs int) string {
	if len(hops) > 0 {
		if HasHopPrefix(hops, "218.105.") || HasHopPrefix(hops, "210.51.") {
			return "联通 9929"
		}
		if HasCN2Hop(hops) {
			return "CN2 GIA"
		}
		if HasHopPrefix(hops, "219.158.") {
			return "联通 4837"
		}
	}
	if latencyMs > 0 {
		if latencyMs < 80 {
			return "联通 BGP 直连"
		}
		return "联通普通直连"
	}
	return "联通直连"
}

// AnalyzeMobileRoute evaluates intermediate IP hops to identify China Mobile route quality.
func AnalyzeMobileRoute(hops []string, latencyMs int) string {
	if len(hops) > 0 {
		for _, ip := range hops {
			if strings.HasPrefix(ip, "223.120.16.") || strings.HasPrefix(ip, "223.120.17.") ||
				strings.HasPrefix(ip, "223.120.18.") || strings.HasPrefix(ip, "223.120.19.") {
				return "移动 CMIN2"
			}
		}
		if HasCN2Hop(hops) {
			return "CN2 GIA"
		}
		if HasHopPrefix(hops, "221.183.") || HasHopPrefix(hops, "223.120.") {
			return "移动 CMI"
		}
	}
	if latencyMs > 0 {
		if latencyMs < 80 {
			return "移动 BGP 直连"
		}
		return "移动普通直连"
	}
	return "移动直连"
}
