package control_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liyuwei007036/polaris/internal/control"
)

func TestSubscriptionReflectsProxyGroupModifications(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)

	// Create 2 nodes with listeners and endpoints
	var endpointIDs []string
	for index, name := range []string{"Node-A", "Node-B"} {
		nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, name)
		if err := store.SetNodeClientAddress(t.Context(), nodeID, "node"+string(rune('a'+index))+".example.com"); err != nil {
			t.Fatal(err)
		}
		listener, err := store.CreateListener(t.Context(), control.Listener{
			NodeID: nodeID, Name: "VLESS " + name, Domain: "listen-" + string(rune('a'+index)) + ".example.com",
			ListenAddr: "0.0.0.0", Port: uint16(2000 + index), Enabled: true,
			Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		endpoint, err := store.CreateEndpoint(t.Context(), control.Endpoint{ListenerID: listener.ID, Name: "用户1", Alias: "节点 " + name, Enabled: true},
			control.EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a966" + string(rune('1'+index))})
		if err != nil {
			t.Fatal(err)
		}
		endpointIDs = append(endpointIDs, endpoint.ID)
	}

	// 1. Create proxy group G1 with endpoint 0
	g1, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "我的代理组", Strategy: "select", Members: []control.MihomoGroupMember{{Kind: "endpoint", ID: endpointIDs[0]}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Create client config referencing G1
	config, err := store.CreateMihomoClientConfig(t.Context(), control.MihomoClientConfig{
		Name:          "默认订阅",
		ProxyGroupIDs: []string{g1.ID},
		RuleMode:      "table",
		Rules: []control.MihomoRule{
			{Type: "MATCH", Action: "我的代理组"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Fetch subscription
	resp1 := request(t, http.MethodGet, httpServer.URL+config.SubscriptionPath, nil, "", "")
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp1.StatusCode)
	}
	etag1 := resp1.Header.Get("ETag")
	if etag1 == "" {
		t.Fatalf("expected non-empty ETag header")
	}
	if cc := resp1.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") || !strings.Contains(cc, "no-store") {
		t.Fatalf("expected Cache-Control to contain no-cache, no-store, got: %s", cc)
	}
	if pragma := resp1.Header.Get("Pragma"); pragma != "no-cache" {
		t.Fatalf("expected Pragma: no-cache, got: %s", pragma)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()

	// Conditional request with ETag should return 304 Not Modified
	req304, err := http.NewRequest(http.MethodGet, httpServer.URL+config.SubscriptionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req304.Header.Set("If-None-Match", etag1)
	resp304, err := http.DefaultClient.Do(req304)
	if err != nil {
		t.Fatal(err)
	}
	if resp304.StatusCode != http.StatusNotModified {
		t.Fatalf("expected status 304 Not Modified, got: %d", resp304.StatusCode)
	}
	resp304.Body.Close()

	// 3. User modifies proxy group via HTTP PUT /api/v1/mihomo/proxy-groups/{id}
	// Change strategy to url-test, add endpoint 1
	updatePayload := map[string]any{
		"name":     "我的代理组",
		"strategy": "url-test",
		"members": []map[string]string{
			{"kind": "endpoint", "id": endpointIDs[0]},
			{"kind": "endpoint", "id": endpointIDs[1]},
		},
	}
	updateResp := request(t, http.MethodPut, httpServer.URL+"/api/v1/mihomo/proxy-groups/"+g1.ID, updatePayload, session, csrfToken)
	if updateResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(updateResp.Body)
		t.Fatalf("update proxy group failed %d: %s", updateResp.StatusCode, string(respBody))
	}
	updateResp.Body.Close()

	// Fetch subscription again - should return 200 with new content and different ETag
	reqNew, _ := http.NewRequest(http.MethodGet, httpServer.URL+config.SubscriptionPath, nil)
	reqNew.Header.Set("If-None-Match", etag1)
	resp2, err := http.DefaultClient.Do(reqNew)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK after update, got: %d", resp2.StatusCode)
	}
	etag2 := resp2.Header.Get("ETag")
	if etag2 == etag1 {
		t.Fatalf("expected ETag to change after proxy group modification, but remained: %s", etag2)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if !strings.Contains(string(body2), "type: url-test") {
		t.Fatalf("expected 'type: url-test' in updated subscription YAML:\n%s", string(body2))
	}
	if !strings.Contains(string(body2), "节点 Node-B") {
		t.Fatalf("expected '节点 Node-B' in updated subscription YAML:\n%s", string(body2))
	}

	// 4. Test client config referencing multiple groups
	g2, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "第二代理组", Strategy: "fallback", Members: []control.MihomoGroupMember{{Kind: "endpoint", ID: endpointIDs[1]}},
	})
	if err != nil {
		t.Fatal(err)
	}

	clientConfigPayload := map[string]any{
		"name":            "默认订阅",
		"proxy_group_ids": []string{g1.ID, g2.ID},
		"rule_mode":       "table",
		"rules": []map[string]any{
			{"type": "MATCH", "action": "第二代理组"},
		},
	}
	configUpdateResp := request(t, http.MethodPut, httpServer.URL+"/api/v1/mihomo/client-configs/"+config.ID, clientConfigPayload, session, csrfToken)
	if configUpdateResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(configUpdateResp.Body)
		t.Fatalf("update client config failed %d: %s", configUpdateResp.StatusCode, string(respBody))
	}
	configUpdateResp.Body.Close()

	resp3 := request(t, http.MethodGet, httpServer.URL+config.SubscriptionPath, nil, "", "")
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp3.StatusCode)
	}
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()

	if !strings.Contains(string(body3), "name: 第二代理组") {
		t.Fatalf("expected 'name: 第二代理组' in subscription YAML:\n%s", string(body3))
	}

	// 5. Test text mode rules rewrite on proxy group rename
	textConfig, err := store.CreateMihomoClientConfig(t.Context(), control.MihomoClientConfig{
		Name:          "文本模式订阅",
		ProxyGroupIDs: []string{g2.ID},
		RuleMode:      "text",
		RawRules:      "- MATCH, 第二代理组",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Rename g2 from "第二代理组" to "重命名分组"
	renamePayload := map[string]any{
		"name":     "重命名分组",
		"strategy": "fallback",
		"members": []map[string]string{
			{"kind": "endpoint", "id": endpointIDs[1]},
		},
	}
	renameResp := request(t, http.MethodPut, httpServer.URL+"/api/v1/mihomo/proxy-groups/"+g2.ID, renamePayload, session, csrfToken)
	if renameResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(renameResp.Body)
		t.Fatalf("rename proxy group failed %d: %s", renameResp.StatusCode, string(respBody))
	}
	renameResp.Body.Close()

	// Text mode subscription must succeed and have the rewritten group name in rules
	respText := request(t, http.MethodGet, httpServer.URL+textConfig.SubscriptionPath, nil, "", "")
	if respText.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(respText.Body)
		t.Fatalf("subscription fetch after rename failed %d: %s", respText.StatusCode, string(respBody))
	}
	bodyText, _ := io.ReadAll(respText.Body)
	respText.Body.Close()

	if !strings.Contains(string(bodyText), "name: 重命名分组") {
		t.Fatalf("expected 'name: 重命名分组' in subscription YAML:\n%s", string(bodyText))
	}
	if !strings.Contains(string(bodyText), "MATCH,重命名分组") {
		t.Fatalf("expected 'MATCH,重命名分组' in rules:\n%s", string(bodyText))
	}

	_ = body1
}

func TestClientSubscriptionETagAndConditionalRequests(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)

	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "node-sub")
	if err := store.SetNodeClientAddress(t.Context(), nodeID, "sub.example.com"); err != nil {
		t.Fatal(err)
	}
	listener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "VLESS", Domain: "sub.example.com",
		ListenAddr: "0.0.0.0", Port: 8443, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := store.CreateEndpoint(t.Context(), control.Endpoint{ListenerID: listener.ID, Name: "user1", Enabled: true},
		control.EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"})
	if err != nil {
		t.Fatal(err)
	}

	// Create subscription
	sub, token, err := store.CreateSubscription(t.Context(), control.SubscriptionInput{
		Kind:        control.ClientSubscription,
		Name:        "通用客户端订阅",
		EndpointIDs: []string{endpoint.ID},
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = sub

	subURL := httpServer.URL + "/api/v1/subscriptions/access/" + token
	resp, err := http.Get(subURL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatalf("expected non-empty ETag")
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") || !strings.Contains(cc, "no-store") {
		t.Fatalf("expected Cache-Control to contain no-cache, no-store, got: %s", cc)
	}
	if pragma := resp.Header.Get("Pragma"); pragma != "no-cache" {
		t.Fatalf("expected Pragma: no-cache, got: %s", pragma)
	}
	resp.Body.Close()

	// Conditional request with If-None-Match
	req, _ := http.NewRequest(http.MethodGet, subURL, nil)
	req.Header.Set("If-None-Match", etag)
	condResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if condResp.StatusCode != http.StatusNotModified {
		t.Fatalf("expected status 304 Not Modified, got: %d", condResp.StatusCode)
	}
	condResp.Body.Close()
}
