package control

import (
	"testing"

	"github.com/liyuwei007036/polaris/internal/wire"
)

// The Clash API names the inbound in metadata.type, never in chains. An agent
// that has not been upgraded yet still sends the chain tail as its inbound
// tag, so the master has to fall back to the metadata rather than show that
// node's connections with an empty 接入服务.
func TestConnectionListenerIDComesFromInboundMetadata(t *testing.T) {
	upgraded := wire.ConnectionInfo{InboundTag: "listener-abc", Inbound: "vless/listener-abc"}
	if id := connectionListenerID(upgraded); id != "abc" {
		t.Fatalf("listener id = %q, want abc from the reported inbound tag", id)
	}
	// An older agent reports the chain tail — an outbound — as the tag.
	legacy := wire.ConnectionInfo{InboundTag: "outbound-hk", Inbound: "vless/listener-abc"}
	if id := connectionListenerID(legacy); id != "abc" {
		t.Fatalf("legacy listener id = %q, want abc parsed out of metadata.type", id)
	}
	// A connection through an inbound the console does not manage has no
	// listener, and inventing one would attach it to the wrong service.
	unmanaged := wire.ConnectionInfo{InboundTag: "outbound-hk", Inbound: "mixed"}
	if id := connectionListenerID(unmanaged); id != "" {
		t.Fatalf("unmanaged listener id = %q, want it empty", id)
	}
}

// The Clash API omits the authenticated user from connection metadata, but the
// matched route rule is reported verbatim and every compiled account carries an
// auth_user condition — so the account name is recoverable from the rule.
func TestConnectionAccountComesFromMatchedRouteRule(t *testing.T) {
	routed := wire.ConnectionInfo{Rule: "inbound=listener-52d9 auth_user=user_11e0a651 => route(outbound-06f5)"}
	if account := connectionAccount(routed); account != "user_11e0a651" {
		t.Fatalf("account = %q, want user_11e0a651 parsed out of the rule", account)
	}
	// auth_user last in the string has no trailing separator to stop at.
	trailing := wire.ConnectionInfo{Rule: "inbound=listener-52d9 auth_user=user_22bd9312"}
	if account := connectionAccount(trailing); account != "user_22bd9312" {
		t.Fatalf("trailing account = %q, want user_22bd9312", account)
	}
	// A connection that matched no account rule has no account to show, and
	// guessing one would attribute traffic to the wrong client node.
	unmatched := wire.ConnectionInfo{Rule: "inbound=listener-52d9 => route(direct)"}
	if account := connectionAccount(unmatched); account != "" {
		t.Fatalf("unmatched account = %q, want it empty", account)
	}
	// An agent new enough to report the user directly is trusted over the rule.
	reported := wire.ConnectionInfo{User: "user_direct", Rule: "auth_user=user_from_rule"}
	if account := connectionAccount(reported); account != "user_direct" {
		t.Fatalf("account = %q, want the directly reported user", account)
	}
}

// The console shows egresses by the name an operator gave them; sing-box only
// knows the compiled tag.
func TestOutboundDisplayNameResolvesConfiguredEgress(t *testing.T) {
	names := map[string]string{"hk1": "香港家宽"}
	cases := map[string]string{
		"outbound-hk1": "香港家宽",
		"direct":       "直连",
		"outbound-999": "outbound-999",
		"":             "",
	}
	for tag, want := range cases {
		if got := outboundDisplayName(tag, names); got != want {
			t.Fatalf("outbound %q displayed as %q, want %q", tag, got, want)
		}
	}
}
