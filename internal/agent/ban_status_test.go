package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestParseBanTimeConvertsLocalWallClockToUTC(t *testing.T) {
	local, err := time.ParseInLocation(banTimeLayout, "2026-08-06 10:00:00", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := parseBanTime("2026-08-06 10:00:00"), local.UTC().Format(time.RFC3339); got != want {
		t.Fatalf("parseBanTime = %q, want %q", got, want)
	}
	// Fail2Ban on an older host prints nothing usable; the console then shows
	// the address without a time rather than a wrong one.
	if got := parseBanTime("not a time"); got != "" {
		t.Fatalf("parseBanTime accepted %q", got)
	}
}

func TestBansWithoutTimesKeepsTheAddresses(t *testing.T) {
	bans := bansWithoutTimes([]string{"203.0.113.7", "198.51.100.9"})
	if len(bans) != 2 || bans[0].IP != "203.0.113.7" || bans[0].BannedAt != "" {
		t.Fatalf("unexpected fallback bans: %#v", bans)
	}
	if bansWithoutTimes(nil) != nil {
		t.Fatal("an empty ban list should stay empty")
	}
}

func TestUnbanRejectsUnsafeInput(t *testing.T) {
	hashed := func(payload map[string]string) Task {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(encoded)
		return Task{ID: "0123456789abcdef0123456789abcdef", Kind: "fail2ban.unban", Payload: string(encoded), ExpectedHash: hex.EncodeToString(digest[:])}
	}
	if result := unbanAddress(context.Background(), hashed(map[string]string{"jail": "polaris-ssh", "ip": "203.0.113.7"})); result.Status == "succeeded" {
		t.Fatalf("unban should not succeed without fail2ban-client: %#v", result)
	}
	if result := unbanAddress(context.Background(), hashed(map[string]string{"jail": "polaris-ssh; rm -rf /", "ip": "203.0.113.7"})); result.Status != "failed" {
		t.Fatalf("accepted an invalid jail name: %#v", result)
	}
	if result := unbanAddress(context.Background(), hashed(map[string]string{"jail": "polaris-ssh", "ip": "not-an-ip"})); result.Status != "failed" {
		t.Fatalf("accepted an invalid address: %#v", result)
	}
	tampered := hashed(map[string]string{"jail": "polaris-ssh", "ip": "203.0.113.7"})
	tampered.ExpectedHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if result := unbanAddress(context.Background(), tampered); result.Status != "failed" {
		t.Fatalf("accepted a payload whose hash does not match: %#v", result)
	}
}
