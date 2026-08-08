package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestFail2BanMutationsRejectTamperedOrUnsafePayloads(t *testing.T) {
	hashed := func(mutation Fail2BanMutation) Task {
		encoded, err := json.Marshal(mutation)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(encoded)
		return Task{ID: "0123456789abcdef0123456789abcdef", Kind: "fail2ban.mutate", Payload: string(encoded), ExpectedHash: hex.EncodeToString(digest[:])}
	}
	valid := Fail2BanMutation{Operation: "save", Jail: LiveFail2BanJail{
		Name: "test", FilterName: "test", LogPath: "/var/log/auth.log", FailRegex: "x <HOST>",
		MaxRetry: 5, FindTimeSeconds: 600, BanTimeSeconds: 600,
	}}
	tampered := hashed(valid)
	tampered.ExpectedHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if result := mutateFail2Ban(context.Background(), tampered); result.Status != "failed" {
		t.Fatalf("accepted payload with a wrong hash: %#v", result)
	}
	// A jail name is written straight into an INI section and a filter file
	// name, so anything that could escape either has to be refused.
	escaping := valid
	escaping.Jail.FilterName = "../evil"
	if result := mutateFail2Ban(context.Background(), hashed(escaping)); result.Status != "failed" {
		t.Fatalf("accepted a filter name outside the managed namespace: %#v", result)
	}
	unknown := valid
	unknown.Operation = "drop-everything"
	if result := mutateFail2Ban(context.Background(), hashed(unknown)); result.Status != "failed" {
		t.Fatalf("accepted an unsupported operation: %#v", result)
	}
	malformed := Task{ID: "0123456789abcdef0123456789abcdef", Kind: "fail2ban.mutate", Payload: "not-json", ExpectedHash: "00"}
	if result := mutateFail2Ban(context.Background(), malformed); result.Status != "failed" {
		t.Fatalf("accepted invalid payload: %#v", result)
	}
}

func TestFirewallMutationsRejectTamperedPayloads(t *testing.T) {
	mutation := FirewallMutation{Operation: "add", Rule: LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 443}}
	encoded, err := json.Marshal(mutation)
	if err != nil {
		t.Fatal(err)
	}
	tampered := Task{ID: "0123456789abcdef0123456789abcdef", Kind: "firewall.mutate", Payload: string(encoded), ExpectedHash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}
	if result := mutateFirewall(context.Background(), tampered); result.Status != "failed" {
		t.Fatalf("accepted payload with a wrong hash: %#v", result)
	}
	malformed := Task{ID: tampered.ID, Kind: tampered.Kind, Payload: "not-json", ExpectedHash: "00"}
	if result := mutateFirewall(context.Background(), malformed); result.Status != "failed" {
		t.Fatalf("accepted invalid payload: %#v", result)
	}
}
