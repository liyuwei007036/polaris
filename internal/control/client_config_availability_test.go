package control_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/liyuwei007036/polaris/internal/control"
)

// A user or service that is switched off is a reversible, everyday state. It
// must not take the client configurations that reference it down with it: the
// subscription keeps serving the members that are still usable, and the
// configuration stays editable so the operator can repair it.
func TestUnavailableGroupMemberKeepsItsClientConfigUsable(t *testing.T) {
	store, server, baseURL, session, csrfToken := newFeatureServer(t)
	nodeID := approveTestNode(t, server, baseURL, session, csrfToken, "availability-node")
	if err := store.SetNodeClientAddress(t.Context(), nodeID, "avail.example.com"); err != nil {
		t.Fatal(err)
	}
	listener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "接入服务", Domain: "avail.example.com", ListenAddr: "0.0.0.0", Port: 8443, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	kept, err := store.CreateEndpoint(t.Context(), control.Endpoint{
		ListenerID: listener.ID, Name: "账号甲", Alias: "节点甲", Enabled: true,
	}, control.EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"})
	if err != nil {
		t.Fatal(err)
	}
	switchedOff, err := store.CreateEndpoint(t.Context(), control.Endpoint{
		ListenerID: listener.ID, Name: "账号乙", Alias: "节点乙", Enabled: true,
	}, control.EndpointCredentials{UUID: "8a7d1e2f-3b4c-4d5e-9f60-1a2b3c4d5e6f"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "可用性分组", Strategy: "select",
		Members: []control.MihomoGroupMember{{Kind: "endpoint", ID: kept.ID}, {Kind: "endpoint", ID: switchedOff.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"name": "可用性配置", "proxy_group_ids": []string{group.ID}, "rule_mode": "table",
		"rules": []map[string]any{{"type": "MATCH", "action": group.Name}},
	}
	var config struct {
		ID               string `json:"id"`
		SubscriptionPath string `json:"subscription_path"`
	}
	response := request(t, http.MethodPost, baseURL+"/api/v1/mihomo/client-configs", payload, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create client config: got %d", response.StatusCode)
	}
	decodeBody(t, response, &config)

	response = request(t, http.MethodGet, baseURL+config.SubscriptionPath, nil, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("subscription before switching a user off: got %d", response.StatusCode)
	}
	if yaml := readBodyForTest(t, response); !strings.Contains(yaml, "节点甲") || !strings.Contains(yaml, "节点乙") {
		t.Fatalf("subscription is missing a node:\n%s", yaml)
	}

	if err := store.SetEndpointEnabled(t.Context(), switchedOff.ID, false); err != nil {
		t.Fatal(err)
	}

	// The group naming the switched-off user has to stay saveable as it is,
	// otherwise switching that user back on means rebuilding the group.
	response = request(t, http.MethodPut, baseURL+"/api/v1/mihomo/proxy-groups/"+group.ID, map[string]any{
		"name": group.Name, "strategy": "select",
		"members": []map[string]any{{"kind": "endpoint", "id": kept.ID}, {"kind": "endpoint", "id": switchedOff.ID}},
	}, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save the group holding a switched-off member: got %d, want 200", response.StatusCode)
	}

	response = request(t, http.MethodPut, baseURL+"/api/v1/mihomo/client-configs/"+config.ID, payload, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("edit config with a switched-off member: got %d, want 200", response.StatusCode)
	}
	response = request(t, http.MethodPost, baseURL+"/api/v1/mihomo/client-configs/"+config.ID+"/copy", map[string]any{"name": "可用性配置副本"}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("copy config with a switched-off member: got %d, want 201", response.StatusCode)
	}

	response = request(t, http.MethodGet, baseURL+config.SubscriptionPath, nil, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("subscription after switching a user off: got %d, want 200", response.StatusCode)
	}
	yaml := readBodyForTest(t, response)
	if !strings.Contains(yaml, "节点甲") {
		t.Fatalf("subscription dropped the user that is still on:\n%s", yaml)
	}
	if strings.Contains(yaml, "节点乙") {
		t.Fatalf("subscription still carries the switched-off user:\n%s", yaml)
	}

	// With every user in the group switched off the group still has to carry an
	// entry, or the client refuses the whole profile.
	if err := store.SetEndpointEnabled(t.Context(), kept.ID, false); err != nil {
		t.Fatal(err)
	}
	response = request(t, http.MethodGet, baseURL+config.SubscriptionPath, nil, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("subscription with every user switched off: got %d, want 200", response.StatusCode)
	}
	if yaml := readBodyForTest(t, response); !strings.Contains(yaml, "REJECT") {
		t.Fatalf("group with no usable user has no entry:\n%s", yaml)
	}
}

// Removing a server only marks it revoked; the accounts it hosted stay in the
// proxy groups that named them. Those configurations must stay usable too.
func TestRevokedNodeKeepsItsClientConfigUsable(t *testing.T) {
	store, server, baseURL, session, csrfToken := newFeatureServer(t)
	keptNode := approveTestNode(t, server, baseURL, session, csrfToken, "kept-node")
	goneNode := approveTestNode(t, server, baseURL, session, csrfToken, "gone-node")
	for _, nodeID := range []string{keptNode, goneNode} {
		if err := store.SetNodeClientAddress(t.Context(), nodeID, nodeID+".example.com"); err != nil {
			t.Fatal(err)
		}
	}
	members := []control.MihomoGroupMember{}
	for index, nodeID := range []string{keptNode, goneNode} {
		listener, err := store.CreateListener(t.Context(), control.Listener{
			NodeID: nodeID, Name: "接入服务", Domain: nodeID + ".example.com", ListenAddr: "0.0.0.0", Port: 8443, Enabled: true,
			Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		alias := []string{"留下的节点", "撤销的节点"}[index]
		endpoint, err := store.CreateEndpoint(t.Context(), control.Endpoint{
			ListenerID: listener.ID, Name: "默认账号", Alias: alias, Enabled: true,
		}, control.EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a966" + string(rune('0'+index))})
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, control.MihomoGroupMember{Kind: "endpoint", ID: endpoint.ID})
	}
	group, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "跨服务器分组", Strategy: "select", Members: members,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"name": "跨服务器配置", "proxy_group_ids": []string{group.ID}, "rule_mode": "table",
		"rules": []map[string]any{{"type": "MATCH", "action": group.Name}},
	}
	var config struct {
		ID               string `json:"id"`
		SubscriptionPath string `json:"subscription_path"`
	}
	response := request(t, http.MethodPost, baseURL+"/api/v1/mihomo/client-configs", payload, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create client config: got %d", response.StatusCode)
	}
	decodeBody(t, response, &config)

	if err := store.RevokeNode(t.Context(), goneNode); err != nil {
		t.Fatal(err)
	}

	response = request(t, http.MethodGet, baseURL+config.SubscriptionPath, nil, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("subscription after removing a server: got %d, want 200", response.StatusCode)
	}
	yaml := readBodyForTest(t, response)
	if !strings.Contains(yaml, "留下的节点") || strings.Contains(yaml, "撤销的节点") {
		t.Fatalf("subscription does not reflect the removed server:\n%s", yaml)
	}
	response = request(t, http.MethodPut, baseURL+"/api/v1/mihomo/client-configs/"+config.ID, payload, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("edit config after removing a server: got %d, want 200", response.StatusCode)
	}
	response = request(t, http.MethodPost, baseURL+"/api/v1/mihomo/client-configs/"+config.ID+"/copy", map[string]any{"name": "跨服务器配置副本"}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("copy config after removing a server: got %d, want 201", response.StatusCode)
	}
}
