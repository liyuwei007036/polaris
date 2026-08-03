package control

import (
	"strings"
	"testing"
	"time"
)

func TestReconcileNodeDesiredStateQueuesOnlyDriftedConfigurations(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	operator, _, err := store.EnsureDefaultAdmin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateRegistrationToken(t.Context(), operator.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := store.RegisterAgent(t.Context(), RegistrationInput{
		Token: token.Token, NodeName: "reconcile-node", PublicKey: []byte(strings.Repeat("k", 32)), Capabilities: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := store.ApproveRegistration(t.Context(), registration.ID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	_, singBoxHash, err := store.CompileNodeConfig(t.Context(), approved.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	_, nginxHash, err := store.CompileNodeNginx(t.Context(), approved.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	server.reconcileNodeDesiredState(t.Context(), approved.NodeID, "1.13.0", singBoxHash, nginxHash)
	if tasks, _, err := store.ListTasks(t.Context(), approved.NodeID, "", 1, 20); err != nil || len(tasks) != 0 {
		t.Fatalf("matching configuration hashes queued tasks: tasks=%#v err=%v", tasks, err)
	}

	server.reconcileNodeDesiredState(t.Context(), approved.NodeID, "1.13.0", "stale", "stale")
	tasks, _, err := store.ListTasks(t.Context(), approved.NodeID, "", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("drifted configurations queued %d tasks, want 2: %#v", len(tasks), tasks)
	}
	kinds := map[string]bool{}
	for _, task := range tasks {
		kinds[task.Kind] = true
	}
	if !kinds["singbox.apply_config"] || !kinds["nginx.apply_config"] {
		t.Fatalf("reconciliation task kinds = %#v", kinds)
	}
	server.reconcileNodeDesiredState(t.Context(), approved.NodeID, "1.13.0", "stale", "stale")
	if tasks, _, err = store.ListTasks(t.Context(), approved.NodeID, "", 1, 20); err != nil || len(tasks) != 2 {
		t.Fatalf("repeated drift report duplicated tasks: tasks=%#v err=%v", tasks, err)
	}
}
