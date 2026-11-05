package control

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

// answerQuestions stands in for a connected node: it counts how many questions
// actually reached it and answers each one after a delay, so two overlapping
// requests have time to meet in flight.
func answerQuestions(t *testing.T, server *Server, session *controlSession, delay time.Duration) *int32 {
	t.Helper()
	var asked int32
	go func() {
		for {
			select {
			case question := <-session.questions:
				atomic.AddInt32(&asked, 1)
				time.Sleep(delay)
				server.deliverTaskAnswer(wire.TaskResult{TaskID: question.ID, Status: "succeeded", Data: "{}"})
			case <-session.done:
				return
			}
		}
	}()
	return &asked
}

func newAnsweredServer(t *testing.T) (*Server, *controlSession) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	session := &controlSession{done: make(chan struct{}), questions: make(chan wire.Task, 8)}
	server.controls["node-1"] = session
	t.Cleanup(func() { close(session.done) })
	return server, session
}

// The security screen reads the jail list and the banned list in two requests,
// and both need the same fail2ban.query answer. Asking the node twice made the
// page wait for the same work to be done again; the second request must join
// the one already in flight.
func TestConcurrentIdenticalReadsShareOneRoundTrip(t *testing.T) {
	server, session := newAnsweredServer(t)
	asked := answerQuestions(t, server, session, 100*time.Millisecond)

	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			answers := server.askNodes(context.Background(), []string{"node-1"}, "fail2ban.query", "{}")
			results <- answers["node-1"].err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("live query failed: %v", err)
		}
	}
	if got := atomic.LoadInt32(asked); got != 1 {
		t.Fatalf("node received %d identical queries, want 1", got)
	}
}

// Sharing is only for reads. A change must reach the host once per request,
// even when two operators press the button at the same moment.
func TestConcurrentIdenticalMutationsAreNotShared(t *testing.T) {
	server, session := newAnsweredServer(t)
	asked := answerQuestions(t, server, session, 50*time.Millisecond)

	start := make(chan struct{})
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			server.askNodes(context.Background(), []string{"node-1"}, "firewall.mutate", "{}")
		}()
	}
	close(start)
	group.Wait()
	if got := atomic.LoadInt32(asked); got != 2 {
		t.Fatalf("node received %d mutations, want 2", got)
	}
}

// Nodes that cannot answer must not prevent the other nodes' answers from
// being returned.
func TestAskNodesStillAnswersTheReachableNodes(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	answers := server.askNodes(context.Background(), []string{"missing"}, "firewall.query", "{}")
	if answers["missing"].err == nil {
		t.Fatal("an offline node reported an answer it never sent")
	}
}

// A screen may give the whole read less time than one node's live-task budget.
// The page must then say the node did not answer in time instead of waiting for
// the longer per-node timeout.
func TestAskNodeHonoursAShorterScreenDeadline(t *testing.T) {
	server, _ := newAnsweredServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := server.AskNode(ctx, "node-1", "firewall.query", "{}")
	if err == nil || !strings.Contains(err.Error(), "未在限定时间内响应") {
		t.Fatalf("deadline error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("AskNode waited %v for a node that never answered", elapsed)
	}
}
