package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestApplyFail2BanRejectsTamperedOrUnsafePayloads(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"jail":    "[polaris-test]\nenabled = true\n",
		"filters": map[string]string{"polaris-test.conf": "[Definition]\nfailregex = x <HOST>\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	task := Task{ID: "0123456789abcdef0123456789abcdef", Kind: "fail2ban.apply", Payload: string(payload), ExpectedHash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}
	if result := applyFail2Ban(context.Background(), task); result.Status != "failed" {
		t.Fatalf("accepted payload with a wrong hash: %#v", result)
	}
	escaping, err := json.Marshal(map[string]any{
		"jail":    "[polaris-test]\n",
		"filters": map[string]string{"../evil.conf": "[Definition]\nfailregex = x\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	escapingDigest := sha256.Sum256(escaping)
	task = Task{ID: task.ID, Kind: task.Kind, Payload: string(escaping), ExpectedHash: hex.EncodeToString(escapingDigest[:])}
	if result := applyFail2Ban(context.Background(), task); result.Status != "failed" {
		t.Fatalf("accepted filter outside the managed namespace: %#v", result)
	}
	task = Task{ID: task.ID, Kind: task.Kind, Payload: "not-json", ExpectedHash: hex.EncodeToString(digest[:])}
	if result := applyFail2Ban(context.Background(), task); result.Status != "failed" {
		t.Fatalf("accepted invalid payload: %#v", result)
	}
}
