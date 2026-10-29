package agent

import (
	"testing"
)

func TestParseMTRJSONOutput(t *testing.T) {
	jsonSample := `{
		"report": {
			"hubs": [
				{"count": 1, "host": "10.0.0.1", "Loss%": 0.0, "Snt": 10, "Avg": 1.2, "Best": 0.8, "Wrst": 2.1, "StDev": 0.3},
				{"count": 2, "host": "59.43.18.1", "Loss%": 20.0, "Snt": 10, "Avg": 35.5, "Best": 34.0, "Wrst": 45.0, "StDev": 2.5},
				{"count": 3, "host": "???", "Loss%": 100.0, "Snt": 10, "Avg": 0.0, "Best": 0.0, "Wrst": 0.0, "StDev": 0.0},
				{"count": 4, "host": "202.96.209.133", "Loss%": 0.0, "Snt": 10, "Avg": 145.0, "Best": 143.0, "Wrst": 150.0, "StDev": 1.5}
			]
		}
	}`

	hops := parseMTRJSONOutput([]byte(jsonSample), 10)
	if len(hops) != 4 {
		t.Fatalf("expected 4 hops, got %d", len(hops))
	}
	if hops[0].AvgMs != 1.2 || hops[0].LossPct != 0.0 {
		t.Errorf("unexpected hop 1: %+v", hops[0])
	}
	if hops[1].LossPct != 20.0 || hops[1].IP != "59.43.18.1" {
		t.Errorf("unexpected hop 2: %+v", hops[1])
	}

	enrichMTRHops(hops)

	// Hop 2 (59.43.18.1) has loss 20%, but subsequent hop 4 has loss 0%, so hop 2 MUST be identified as rate-limited!
	if !hops[1].IsRateLimited {
		t.Errorf("expected hop 2 to be marked as rate-limited, but got false")
	}
	if hops[1].ASN != "AS4809" {
		t.Errorf("expected hop 2 ASN to be AS4809, got %q", hops[1].ASN)
	}

	report := buildMTRReport("202.96.209.133", "上海电信", hops, "")
	if report.EndToEndLoss != 0.0 {
		t.Errorf("expected end-to-end loss 0.0, got %f", report.EndToEndLoss)
	}
}

func TestParseMTRTextOutput(t *testing.T) {
	textSample := `Start: 2026-10-29T12:00:00+0000
HOST: test-vps                    Loss%   Snt   Last   Avg  Best  Wrst StDev
  1.|-- 198.51.100.1               0.0%    10    0.8   0.9   0.7   1.5   0.2
  2.|-- 219.158.11.1              10.0%    10   50.1  51.0  49.5  60.2   3.1
  3.|-- 210.22.97.1                0.0%    10  135.2 136.0 134.5 140.0   1.8`

	hops := parseMTRTextOutput(textSample, 10)
	if len(hops) != 3 {
		t.Fatalf("expected 3 hops, got %d", len(hops))
	}
	if hops[1].LossPct != 10.0 || hops[1].AvgMs != 51.0 {
		t.Errorf("unexpected hop 2: %+v", hops[1])
	}

	enrichMTRHops(hops)
	if hops[1].ASN != "AS4837" {
		t.Errorf("expected hop 2 ASN AS4837, got %q", hops[1].ASN)
	}
	if !hops[1].IsRateLimited {
		t.Errorf("expected hop 2 to be marked as rate-limited")
	}
}

func TestParseMTRJSONWithStringFields(t *testing.T) {
	// Some versions of mtr format numbers as JSON strings
	jsonSample := `{
		"report": {
			"hubs": [
				{"count": "1", "host": "10.0.0.1", "Loss%": "0.0%", "Snt": "10", "Avg": "1.5", "Best": "1.0", "Wrst": "2.0", "StDev": "0.2"},
				{"count": "2", "host": "202.97.108.1", "Loss%": "0.0", "Snt": "10", "Avg": "15.0", "Best": "14.0", "Wrst": "16.0", "StDev": "0.5"}
			]
		}
	}`

	hops := parseMTRJSONOutput([]byte(jsonSample), 10)
	if len(hops) != 2 {
		t.Fatalf("expected 2 hops, got %d", len(hops))
	}
	if hops[0].Hop != 1 || hops[0].LossPct != 0.0 || hops[0].AvgMs != 1.5 {
		t.Errorf("unexpected hop 0: %+v", hops[0])
	}
	if hops[1].Hop != 2 || hops[1].IP != "202.97.108.1" || hops[1].AvgMs != 15.0 {
		t.Errorf("unexpected hop 1: %+v", hops[1])
	}
}

