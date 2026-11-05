package control_test

import (
	"testing"

	"github.com/liyuwei007036/polaris/internal/control"
)

// Demo seeding runs on every master start. It may add sample nodes, but it must
// never rewrite an operator's credentials: account security settings belong to
// that operator's row and have to survive a restart.
func TestDemoSeedingPreservesOperatorSecurityState(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	admin, _, err := store.EnsureDefaultAdmin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResetOperatorTOTP(t.Context(), admin.ID); err != nil {
		t.Fatal(err)
	}

	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnsureDemoNodes(t.Context()); err != nil {
		t.Fatal(err)
	}

	operators, err := store.ListOperators(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, operator := range operators {
		if operator.ID != admin.ID {
			continue
		}
		if !operator.TOTPEnabled {
			t.Fatal("demo seeding disabled an operator's two-factor authentication")
		}
		if !operator.MustChangePassword {
			t.Fatal("demo seeding cleared an operator's mandatory password change")
		}
		return
	}
	t.Fatal("administrator disappeared after demo seeding")
}
