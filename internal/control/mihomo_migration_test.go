package control

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sb-control/sb-control/internal/security"
)

func TestLegacyMihomoConfigMigratesOnceWithoutResurrection(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	nodeID, _ := newID()
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, client_address, revoked_at, created_at) VALUES (?, ?, ?, ?, NULL, ?)`, nodeID, "迁移节点", make([]byte, 32), "legacy.example.com", nowUnix()); err != nil {
		t.Fatal(err)
	}
	listener, err := store.CreateListener(t.Context(), Listener{
		NodeID: nodeID, Name: "迁移接入", ListenAddr: "0.0.0.0", Port: 1080, Enabled: true,
		Spec: ProtocolSpec{Protocol: "socks", Network: "tcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := store.CreateEndpoint(t.Context(), Endpoint{ListenerID: listener.ID, Name: "旧用户", Enabled: true}, EndpointCredentials{Username: "user", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateMihomoProxyGroup(t.Context(), MihomoProxyGroup{
		Name: "旧节点组", Strategy: "select", EndpointIDs: []string{endpoint.ID}, Aliases: map[string]string{endpoint.ID: "迁移后的别名"},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateMihomoRoutingProfile(t.Context(), MihomoRoutingProfile{Name: "旧规则", RulePreset: "china-direct"})
	if err != nil {
		t.Fatal(err)
	}
	configID, _ := newID()
	groups, _ := json.Marshal([]string{group.ID})
	token, _ := security.RandomToken(32)
	encrypted, _ := security.Encrypt(store.masterKey, []byte(token))
	if _, err := store.db.Exec(`INSERT INTO mihomo_client_configs
		(id, name, proxy_group_ids, routing_profile_id, subscription_token_hash, subscription_token_encrypted, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, configID, "旧客户端配置", string(groups), profile.ID, security.TokenHash(token), encrypted, nowUnix(), nowUnix()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	configs, err := store.ListMihomoClientConfigs(t.Context())
	if err != nil || len(configs) != 1 || len(configs[0].ProxyGroupIDs) != 1 {
		t.Fatalf("migrated configs = %#v, %v", configs, err)
	}
	migratedGroups, err := store.ListMihomoProxyGroups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var referenced *MihomoProxyGroup
	for index := range migratedGroups {
		if migratedGroups[index].ID == configs[0].ProxyGroupIDs[0] {
			referenced = &migratedGroups[index]
		}
	}
	if referenced == nil || len(referenced.Members) != 1 || referenced.Members[0].ID != endpoint.ID {
		t.Fatalf("migrated proxy groups = %#v", migratedGroups)
	}
	if configs[0].RuleMode != "table" || len(configs[0].Rules) != 3 || configs[0].Rules[2].Type != "MATCH" {
		t.Fatalf("migrated rules = %#v", configs[0])
	}
	_, yaml, err := store.GenerateStoredMihomoYAML(t.Context(), configs[0].ID)
	if err != nil || !strings.Contains(yaml, `"name":"迁移后的别名"`) {
		t.Fatalf("legacy alias was not migrated: %v\n%s", err, yaml)
	}
	if err := store.DeleteMihomoClientConfig(t.Context(), configs[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	configs, err = store.ListMihomoClientConfigs(t.Context())
	if err != nil || len(configs) != 0 {
		t.Fatalf("deleted migrated config was resurrected: %#v, %v", configs, err)
	}
}
