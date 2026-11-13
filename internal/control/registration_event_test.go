package control_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/liyuwei007036/polaris/internal/agent"
	"github.com/liyuwei007036/polaris/internal/control"
	"github.com/liyuwei007036/polaris/internal/wire"
)

func TestRegistrationPublishesLiveEvent(t *testing.T) {
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
	agentAddr := startAgentListener(t, server)
	session, csrfToken := login(t, httpServer.URL, secret)

	events, cancel := server.SubscribeLive()
	defer cancel()

	keypair, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}

	token := createRegistrationToken(t, httpServer.URL, session, csrfToken)

	ctx, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDial()
	conn, err := agent.Connect(ctx, agentAddr, keypair, server.NoisePublicKey())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ack, err := agent.Register(conn, token, "pending-node", map[string]string{"os": "linux", "architecture": "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if ack.Status != "pending" {
		t.Fatalf("expected pending status, got %s", ack.Status)
	}

	// Verify that registering publishes Kind: "registration"
	foundRegistrationEvent := false
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()

	for !foundRegistrationEvent {
		select {
		case event := <-events:
			if event.Kind == "registration" {
				foundRegistrationEvent = true
			}
		case <-timer.C:
			t.Fatal("timed out waiting for registration live event")
		}
	}

	// Also verify that approving publishes Kind: "registration" and Kind: "node"
	approveRes := request(t, http.MethodPost, httpServer.URL+"/api/v1/nodes/"+ack.RegistrationID+"/approve", nil, session, csrfToken)
	if approveRes.StatusCode != http.StatusOK {
		t.Fatalf("failed to approve: %d", approveRes.StatusCode)
	}
	approveRes.Body.Close()

	foundApproveRegEvent := false
	foundNodeEvent := false
	timer.Reset(3 * time.Second)

	for !foundApproveRegEvent || !foundNodeEvent {
		select {
		case event := <-events:
			if event.Kind == "registration" {
				foundApproveRegEvent = true
			}
			if event.Kind == "node" {
				foundNodeEvent = true
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for approve events: reg=%v node=%v", foundApproveRegEvent, foundNodeEvent)
		}
	}
}
