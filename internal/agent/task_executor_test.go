package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The security screen asks several questions at once. They are reads, so the
// agent has to answer them together rather than making the page wait for the
// sum of every round trip.
func TestReadOnlyQueriesRunTogether(t *testing.T) {
	executor := newTaskExecutor(TaskOptions{DataDir: t.TempDir()})
	var running, peak int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	executor.execute = func(context.Context, Task, TaskOptions) TaskResult {
		current := atomic.AddInt32(&running, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if current <= old || atomic.CompareAndSwapInt32(&peak, old, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		atomic.AddInt32(&running, -1)
		return TaskResult{Status: "succeeded"}
	}

	tasks := []Task{
		{ID: strings.Repeat("a", 32), Kind: "firewall.query"},
		{ID: strings.Repeat("b", 32), Kind: "fail2ban.query"},
	}
	var group sync.WaitGroup
	for _, task := range tasks {
		group.Add(1)
		go func(task Task) {
			defer group.Done()
			executor.Handle(context.Background(), task)
		}(task)
	}
	for range tasks {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("a read waited for the other read instead of running beside it")
		}
	}
	close(release)
	group.Wait()
	if got := atomic.LoadInt32(&peak); got != 2 {
		t.Fatalf("peak concurrent reads = %d, want 2", got)
	}
}

// A read has a new ID every time a screen opens, so recording its result left
// one file per page view behind forever and never served a later request.
func TestReadOnlyQueriesLeaveNoCompletedRecord(t *testing.T) {
	dataDir := t.TempDir()
	executor := newTaskExecutor(TaskOptions{DataDir: dataDir})
	executor.execute = func(context.Context, Task, TaskOptions) TaskResult {
		return TaskResult{Status: "succeeded", Data: "{}"}
	}
	result := executor.Handle(context.Background(), Task{ID: strings.Repeat("c", 32), Kind: "firewall.query"})
	if result.Status != "succeeded" {
		t.Fatalf("query result = %#v", result)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "completed-tasks"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a read left %d completed-task files behind", len(entries))
	}
}

// A change still holds the host alone: it must wait for reads already running
// and must not let a read see the host halfway through the change.
func TestMutationWaitsForRunningRead(t *testing.T) {
	executor := newTaskExecutor(TaskOptions{DataDir: t.TempDir()})
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	mutationStarted := make(chan struct{})
	executor.execute = func(_ context.Context, task Task, _ TaskOptions) TaskResult {
		if task.Kind == "firewall.query" {
			close(readStarted)
			<-releaseRead
			return TaskResult{Status: "succeeded"}
		}
		close(mutationStarted)
		return TaskResult{Status: "succeeded"}
	}

	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		executor.Handle(context.Background(), Task{ID: strings.Repeat("d", 32), Kind: "firewall.query"})
	}()
	<-readStarted

	group.Add(1)
	go func() {
		defer group.Done()
		executor.Handle(context.Background(), Task{ID: strings.Repeat("e", 32), Kind: "firewall.mutate"})
	}()

	select {
	case <-mutationStarted:
		t.Fatal("a mutation started before the running read finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseRead)
	select {
	case <-mutationStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("the mutation never ran after the read finished")
	}
	group.Wait()
}
