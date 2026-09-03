package agent

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

// fakeMaster stands in for the master at the other end of a real handshaken
// wire connection. One reader goroutine owns the connection for the whole
// test: a reader started per assertion would still be parked in ReadMessage
// after its deadline passed and would swallow the next push, which turns
// "the agent stayed quiet" into something a test cannot tell from
// "the agent pushed and nobody was listening".
type fakeMaster struct {
	conn   *wire.Conn
	pushes chan struct{}
}

func session(t *testing.T, interval time.Duration) *fakeMaster {
	t.Helper()
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

	master := &fakeMaster{conn: masterConn, pushes: make(chan struct{}, 64)}
	go func() {
		for {
			msgType, _, readErr := masterConn.ReadMessage()
			if readErr != nil {
				return
			}
			if msgType == wire.MsgConnections {
				select {
				case master.pushes <- struct{}{}:
				default:
				}
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = RunSession(ctx, agentConn, nil, time.Minute, interval, "1.13.0", t.TempDir()) }()
	t.Cleanup(func() {
		cancel()
		agentRaw.Close()
		masterRaw.Close()
	})
	return master
}

func (m *fakeMaster) awaitPush(within time.Duration) bool {
	select {
	case <-m.pushes:
		return true
	case <-time.After(within):
		return false
	}
}

// settle lets anything already in flight arrive, then forgets it, so the next
// assertion only sees pushes the agent decided to send afterwards.
func (m *fakeMaster) settle(d time.Duration) {
	time.Sleep(d)
	for {
		select {
		case <-m.pushes:
		default:
			return
		}
	}
}

func (m *fakeMaster) tell(t *testing.T, streaming bool) {
	t.Helper()
	body, err := wire.Encode(wire.WatchState{Streaming: streaming})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.conn.WriteMessage(wire.MsgWatch, body); err != nil {
		t.Fatalf("send watch state: %v", err)
	}
}

// A node reports once on connect no matter what — that is what puts it on the
// console the moment it appears — and keeps reporting until told otherwise,
// which is also what keeps it working against a master too old to say.
func TestRunSessionReportsOnConnectAndKeepsGoingUntilTold(t *testing.T) {
	master := session(t, 200*time.Millisecond)
	if !master.awaitPush(5 * time.Second) {
		t.Fatal("no push on connect, want one so a fresh node is not blank on the console")
	}
	if !master.awaitPush(5 * time.Second) {
		t.Fatal("no second push, want the agent reporting by default")
	}
}

// The whole point of the change: a fleet nobody is watching stops polling
// sing-box once a second on every node.
func TestRunSessionGoesQuietWhenNobodyIsWatching(t *testing.T) {
	master := session(t, 200*time.Millisecond)
	if !master.awaitPush(5 * time.Second) {
		t.Fatal("no push on connect")
	}
	master.tell(t, false)
	master.settle(400 * time.Millisecond)
	if master.awaitPush(1500 * time.Millisecond) {
		t.Fatal("agent kept pushing after being told nobody is watching")
	}
}

// And it has to come back the moment a console opens, without waiting out a
// cadence — otherwise the first thing an operator sees is an empty chart.
func TestRunSessionResumesImmediatelyWhenAConsoleOpens(t *testing.T) {
	master := session(t, 10*time.Second) // long, so only the resume can answer
	if !master.awaitPush(5 * time.Second) {
		t.Fatal("no push on connect")
	}
	master.tell(t, false)
	master.settle(300 * time.Millisecond)

	master.tell(t, true)
	if !master.awaitPush(3 * time.Second) {
		t.Fatal("agent did not report straight away when watching resumed")
	}
}
