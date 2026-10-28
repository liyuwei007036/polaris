package wire

import (
	"testing"
)

func TestIPv4ToASN(t *testing.T) {
	tests := []struct {
		ip       string
		expected string
	}{
		// Telecom
		{"59.43.18.2", "AS4809"},
		{"202.97.12.1", "AS4134"},
		{"69.194.1.1", "AS23764"},
		{"203.208.1.1", "AS23764"},

		// Unicom 9929 / CUII
		{"218.105.2.1", "AS9929"},
		{"210.51.3.4", "AS9929"},
		{"210.52.1.1", "AS9929"},
		{"218.107.1.1", "AS9929"},
		{"210.13.1.1", "AS9929"},
		{"218.105.12.1.cuii.unicom.com", "AS9929"},
		{"AS9929-router.net", "AS9929"},

		// Unicom CUG (AS10099)
		{"202.77.1.1", "AS10099"},
		{"43.252.1.1", "AS10099"},
		{"61.14.1.1", "AS10099"},
		{"103.107.1.1", "AS10099"},
		{"43.251.1.1", "AS10099"},

		// Unicom 4837
		{"219.158.1.1", "AS4837"},
		{"221.192.1.1", "AS4837"},

		// Mobile CMIN2
		{"223.118.32.1", "AS58807"},
		{"223.120.16.1", "AS58807"},
		{"223.120.18.1", "AS58807"},
		{"223.120.130.1", "AS58807"},
		{"223.119.8.1", "AS58807"},
		{"223.119.26.1", "AS58807"},

		// Mobile CMI
		{"223.118.1.1", "AS58453"},
		{"223.120.1.1", "AS58453"},
		{"221.183.1.1", "AS9808"},
	}

	for _, tt := range tests {
		got := IPv4ToASN(tt.ip)
		if got != tt.expected {
			t.Errorf("IPv4ToASN(%q) = %q, expected %q", tt.ip, got, tt.expected)
		}
	}
}

func TestAnalyzeUnicomRoute(t *testing.T) {
	// 9929 with 4837 terminal delivery hop
	hops9929 := []string{"10.0.0.1", "218.105.1.1", "219.158.1.1", "210.22.97.1"}
	if r := AnalyzeUnicomRoute(hops9929, 140); r != "联通 9929" {
		t.Fatalf("expected '联通 9929', got %q", r)
	}

	// 10099 (CUG)
	hops10099 := []string{"10.0.0.1", "202.77.1.1", "210.22.97.1"}
	if r := AnalyzeUnicomRoute(hops10099, 140); r != "联通 9929" {
		t.Fatalf("expected '联通 9929', got %q", r)
	}

	// Pure 4837
	hops4837 := []string{"10.0.0.1", "219.158.1.1", "210.22.97.1"}
	if r := AnalyzeUnicomRoute(hops4837, 160); r != "联通 4837" {
		t.Fatalf("expected '联通 4837', got %q", r)
	}

	// Fallback BGP direct (< 60ms)
	if r := AnalyzeUnicomRoute(nil, 45); r != "联通 BGP 直连" {
		t.Fatalf("expected '联通 BGP 直连', got %q", r)
	}

	// Fallback overseas standard backbone (>= 60ms)
	if r := AnalyzeUnicomRoute(nil, 150); r != "联通 4837" {
		t.Fatalf("expected '联通 4837', got %q", r)
	}
}

func TestAnalyzeTelecomRoute(t *testing.T) {
	// CN2 GIA
	hopsGIA := []string{"10.0.0.1", "59.43.1.1", "59.43.2.1", "202.96.209.133"}
	if r := AnalyzeTelecomRoute(hopsGIA, 130); r != "电信 CN2 GIA" {
		t.Fatalf("expected '电信 CN2 GIA', got %q", r)
	}

	// CN2 GT (163 before CN2)
	hopsGT := []string{"10.0.0.1", "202.97.1.1", "59.43.1.1", "202.96.209.133"}
	if r := AnalyzeTelecomRoute(hopsGT, 150); r != "电信 CN2 GT" {
		t.Fatalf("expected '电信 CN2 GT', got %q", r)
	}

	// 跨网 9929 / CUG (重点：国内交接段虽然有 202.97.163，但国际出海核心走 9929，判定为 电信 9929)
	hops9929 := []string{"10.0.0.1", "203.160.75.222", "218.105.2.150", "202.97.81.81", "202.96.209.133"}
	if r := AnalyzeTelecomRoute(hops9929, 146); r != "电信 9929" {
		t.Fatalf("expected '电信 9929', got %q", r)
	}

	// 跨网 CMIN2 (国际出海走 AS58807，国内转接)
	hopsCMIN2 := []string{"10.0.0.1", "223.120.16.1", "202.97.81.81", "202.96.209.133"}
	if r := AnalyzeTelecomRoute(hopsCMIN2, 140); r != "电信 CMIN2" {
		t.Fatalf("expected '电信 CMIN2', got %q", r)
	}

	// 163 backbone
	hops163 := []string{"10.0.0.1", "202.97.1.1", "202.96.209.133"}
	if r := AnalyzeTelecomRoute(hops163, 180); r != "电信 163 骨干" {
		t.Fatalf("expected '电信 163 骨干', got %q", r)
	}
}

func TestAnalyzeMobileRoute(t *testing.T) {
	// CMIN2
	hopsCMIN2 := []string{"10.0.0.1", "223.120.16.1", "120.196.165.24"}
	if r := AnalyzeMobileRoute(hopsCMIN2, 140); r != "移动 CMIN2" {
		t.Fatalf("expected '移动 CMIN2', got %q", r)
	}

	// CMI
	hopsCMI := []string{"10.0.0.1", "223.118.1.1", "120.196.165.24"}
	if r := AnalyzeMobileRoute(hopsCMI, 180); r != "移动 CMI" {
		t.Fatalf("expected '移动 CMI', got %q", r)
	}
}
