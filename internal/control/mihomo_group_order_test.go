package control

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// Mihomo selects the first entry of proxy-groups when it loads a subscription,
// so the document has to lead with the groups the operator picked, in the order
// they picked them, rather than with the groups those only reference.
func TestGeneratedYAMLLeadsWithSelectedProxyGroups(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, client_address, revoked_at, created_at) VALUES (?, ?, ?, ?, NULL, ?)`,
		nodeID, "分组顺序节点", make([]byte, 32), "order.example.com", nowUnix()); err != nil {
		t.Fatal(err)
	}
	listener, err := store.CreateListener(t.Context(), Listener{
		NodeID: nodeID, Name: "入站", ListenAddr: "0.0.0.0", Port: 1080, Enabled: true,
		Spec: ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: TransportOptions{Type: "ws"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	uuids := []string{"bf000d23-0752-40b4-affe-68f7707a9661", "4e05f165-94f3-4f54-aac7-0487dcb83011"}
	aliases := []string{"洛杉矶 01", "东京 01"}
	leafIDs := make([]string, 0, len(aliases))
	for index, alias := range aliases {
		endpoint, err := store.CreateEndpoint(t.Context(),
			Endpoint{ListenerID: listener.ID, Name: alias, Alias: alias, Enabled: true},
			EndpointCredentials{UUID: uuids[index]})
		if err != nil {
			t.Fatal(err)
		}
		group, err := store.CreateMihomoProxyGroup(t.Context(), MihomoProxyGroup{
			Name: []string{"美国节点", "日本节点"}[index], Strategy: "select",
			Members: []MihomoGroupMember{{Kind: "endpoint", ID: endpoint.ID}},
		})
		if err != nil {
			t.Fatal(err)
		}
		leafIDs = append(leafIDs, group.ID)
	}
	rootIDs := make([]string, 0, len(leafIDs))
	for index, name := range []string{"手动选择", "自动选择"} {
		group, err := store.CreateMihomoProxyGroup(t.Context(), MihomoProxyGroup{
			Name: name, Strategy: "select",
			Members: []MihomoGroupMember{{Kind: "group", ID: leafIDs[index]}},
		})
		if err != nil {
			t.Fatal(err)
		}
		rootIDs = append(rootIDs, group.ID)
	}
	// The operator picks the second root first, which is what decides the group
	// the client selects when it loads the subscription.
	config, err := store.CreateMihomoClientConfig(t.Context(), MihomoClientConfig{
		Name: "分组顺序配置", ProxyGroupIDs: []string{rootIDs[1], rootIDs[0]}, RuleMode: "table",
		Rules: []MihomoRule{{Type: "MATCH", Action: "自动选择"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, generated, err := store.GenerateStoredMihomoYAML(t.Context(), config.ID)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		ProxyGroups []struct {
			Name string `yaml:"name"`
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal([]byte(generated), &document); err != nil {
		t.Fatalf("generated YAML cannot be parsed: %v\n%s", err, generated)
	}
	names := make([]string, 0, len(document.ProxyGroups))
	for _, group := range document.ProxyGroups {
		names = append(names, group.Name)
	}
	if len(names) != 4 || names[0] != "自动选择" || names[1] != "手动选择" {
		t.Fatalf("proxy-groups do not lead with the selected groups in order: %v\n%s", names, generated)
	}
}
