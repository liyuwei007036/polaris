package control_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liyuwei007036/polaris/internal/agent"
	"github.com/liyuwei007036/polaris/internal/control"
)

// sing-box is installed automatically exactly once, when a server first comes
// online. Without an upgrade path from the console, whatever version that
// installation picked is the version the server keeps forever.
func TestUpgradeSingBoxDispatchesSignedUpgradeTask(t *testing.T) {
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
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "sing-box-upgrade-node")
	if err := store.UpdateNodeIdentity(t.Context(), nodeID, "0.42.0", "linux", "amd64", "1.13.0", ""); err != nil {
		t.Fatal(err)
	}
	upgradeHash := strings.Repeat("b", 64)
	server.SetLatestSingBoxReleaseForTest(control.SingBoxRelease{
		Version: "1.14.0", Architecture: "amd64", URL: "https://releases.example.invalid/sing-box.tar.gz",
		SHA256: upgradeHash, Enabled: true, Archive: "tar.gz",
	})

	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/nodes/"+nodeID+"/sing-box/install", map[string]string{}, session, csrfToken)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("request sing-box upgrade: got %d", response.StatusCode)
	}
	var task control.Task
	decodeBody(t, response, &task)
	if task.Kind != "singbox.upgrade" || task.ExpectedHash != upgradeHash {
		t.Fatalf("unexpected dispatched task: %#v", task)
	}

	// The agent refuses a bare download URL, so the dispatched payload has to
	// verify against the release signing key agents are handed at approval.
	dataDir := t.TempDir()
	publicKeyPEM, err := store.ReleaseSigningPublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveReleaseSigningPublicKey(dataDir, publicKeyPEM); err != nil {
		t.Fatal(err)
	}
	manifest, err := agent.VerifyReleaseTask(dataDir, agent.Task{Kind: task.Kind, Payload: task.Payload, ExpectedHash: task.ExpectedHash})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "1.14.0" || manifest.Architecture != "amd64" || manifest.Archive != "tar.gz" {
		t.Fatalf("unexpected signed manifest: %#v", manifest)
	}
}

func TestNodeIdentityIPAddresses(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()

	nodeID := "node-ip-test"
	if err := store.ExecForTest(ctx, `INSERT INTO nodes (id, name, public_key, client_address, revoked_at, created_at) VALUES (?, ?, ?, ?, NULL, ?)`,
		nodeID, "node-ip-test", make([]byte, 32), "198.51.100.1", 1000); err != nil {
		t.Fatal(err)
	}

	testIPs := []string{"198.51.100.2", "198.51.100.3", "2001:db8::1"}
	testMappings := map[string]string{
		"198.51.100.2": "10.0.0.121",
	}
	if err := store.UpdateNodeIdentityWithIPs(ctx, nodeID, "0.43.0", "linux", "amd64", "1.14.0", "{}", testIPs, testMappings); err != nil {
		t.Fatalf("UpdateNodeIdentityWithIPs: %v", err)
	}

	node, err := store.NodeByID(ctx, nodeID)
	if err != nil {
		t.Fatalf("NodeByID: %v", err)
	}
	if len(node.IPAddresses) != 3 || node.IPAddresses[0] != "198.51.100.2" || node.IPAddresses[2] != "2001:db8::1" {
		t.Fatalf("unexpected IPAddresses in NodeByID: %#v", node.IPAddresses)
	}
	if node.IPMappings["198.51.100.2"] != "10.0.0.121" {
		t.Fatalf("unexpected IPMappings in NodeByID: %#v", node.IPMappings)
	}

	nodes, err := store.ListNodes(ctx)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 || len(nodes[0].IPAddresses) != 3 {
		t.Fatalf("unexpected IPAddresses in ListNodes: %#v", nodes[0].IPAddresses)
	}

	getNode, err := store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if len(getNode.IPAddresses) != 3 {
		t.Fatalf("unexpected IPAddresses in GetNode: %#v", getNode.IPAddresses)
	}

	testAliases := map[string]string{
		"198.51.100.2": "电信出口",
		"2001:db8::1":  "IPv6直连",
	}
	if err := store.SetNodeIPAliases(ctx, nodeID, testAliases); err != nil {
		t.Fatalf("SetNodeIPAliases: %v", err)
	}

	updatedNode, err := store.NodeByID(ctx, nodeID)
	if err != nil {
		t.Fatalf("NodeByID after alias: %v", err)
	}
	if updatedNode.IPAliases["198.51.100.2"] != "电信出口" || updatedNode.IPAliases["2001:db8::1"] != "IPv6直连" {
		t.Fatalf("unexpected IPAliases in NodeByID: %#v", updatedNode.IPAliases)
	}

	updatedNodes, err := store.ListNodes(ctx)
	if err != nil {
		t.Fatalf("ListNodes after alias: %v", err)
	}
	if updatedNodes[0].IPAliases["198.51.100.2"] != "电信出口" {
		t.Fatalf("unexpected IPAliases in ListNodes: %#v", updatedNodes[0].IPAliases)
	}
}
