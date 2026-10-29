package agent

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

func TestRunSessionExitsWhenMasterSilenced(t *testing.T) {
	masterKeys, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	agentKeys, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	agentRaw, masterRaw := net.Pipe()
	accepted := make(chan *wire.Conn, 1)
	go func() {
		conn, _, acceptErr := wire.AcceptXK(masterRaw, masterKeys)
		if acceptErr != nil {
			accepted <- nil
			return
		}
		accepted <- conn
	}()
	agentConn, err := wire.DialXK(agentRaw, agentKeys, masterKeys.Public)
	if err != nil {
		t.Fatalf("agent handshake: %v", err)
	}
	masterConn := <-accepted
	if masterConn == nil {
		t.Fatal("master handshake failed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() {
		cancel()
		agentRaw.Close()
		masterRaw.Close()
	})

	// Master drains incoming pushes from agent, but never sends any message back to agent (simulating silence)
	go func() {
		for {
			_, _, readErr := masterConn.ReadMessage()
			if readErr != nil {
				return
			}
		}
	}()

	errCh := make(chan error, 1)
	// Pass heartbeatInterval = 50ms so masterSilenceTimeout = 150ms
	go func() {
		errCh <- RunSession(ctx, agentConn, nil, 50*time.Millisecond, time.Second, "1.13.0", t.TempDir())
	}()

	select {
	case sessionErr := <-errCh:
		if sessionErr == nil {
			t.Fatal("expected sessionErr due to master silence, got nil")
		}
		t.Logf("session exited as expected: %v", sessionErr)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for RunSession to exit on master silence")
	}
}
