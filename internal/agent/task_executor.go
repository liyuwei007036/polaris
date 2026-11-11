package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/liyuwei007036/polaris/internal/version"
)

var taskIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type taskExecutor struct {
	options TaskOptions
	// mu serializes tasks that change the host. Reading tasks take it shared,
	// so the several questions one screen asks can be answered at the same
	// time instead of queueing behind each other.
	mu      sync.RWMutex
	execute func(context.Context, Task, TaskOptions) TaskResult
}

type completedTask struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	ExpectedHash string     `json:"expected_hash"`
	Result       TaskResult `json:"result"`
}

func newTaskExecutor(options TaskOptions) *taskExecutor {
	return &taskExecutor{options: options, execute: executeTask}
}

// readOnlyTask reports whether a task only reads host state or measures the
// network. These tasks have no side effects, so several of them can run at
// once; a mutation still waits for every running read to finish and then holds
// the host alone. A long probe must not make the security screen queue behind
// it for its whole timeout.
func readOnlyTask(kind string) bool {
	switch kind {
	case "firewall.query", "fail2ban.query", "outbound.test",
		"network.probe", "probe.network", "probe.tcp":
		return true
	default:
		return false
	}
}

// ephemeralTask reports whether a read's result is worth writing down. The
// security screen's questions carry a fresh ID every time, so their record is
// never read again; storing it only left one file per page view behind.
func ephemeralTask(kind string) bool {
	switch kind {
	case "firewall.query", "fail2ban.query":
		return true
	default:
		return false
	}
}

func (e *taskExecutor) Handle(ctx context.Context, task Task) TaskResult {
	if e.options.DataDir == "" || !taskIDPattern.MatchString(task.ID) {
		return TaskResult{Status: "failed", Summary: "task ID is invalid"}
	}
	execute := e.execute
	if execute == nil {
		execute = executeTask
	}
	// A read is not recorded as a completed task when nothing will ever ask
	// for that ID again. Its ID is new on every page load, so nothing is served
	// from that record — it only left one file per query behind forever.
	// Keeping those reads out of the record also lets them take the shared lock
	// and run together.
	if readOnlyTask(task.Kind) {
		e.mu.RLock()
		defer e.mu.RUnlock()
		if ephemeralTask(task.Kind) {
			return execute(ctx, task, e.options)
		}
		result, found, err := e.completed(task)
		if err != nil {
			return TaskResult{Status: "failed", Summary: "read completed task result: " + err.Error()}
		}
		if !found {
			result = execute(ctx, task, e.options)
			if err := e.saveCompleted(task, result); err != nil {
				return TaskResult{Status: "failed", Summary: "persist task result: " + err.Error()}
			}
		}
		return result
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	result, found, err := e.completed(task)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "read completed task result: " + err.Error()}
	}
	if found && task.Kind == "agent.upgrade" && result.Status == "succeeded" {
		if manifest, vErr := VerifyReleaseTask(e.options.DataDir, task); vErr == nil {
			targetVersion := strings.TrimPrefix(strings.TrimSpace(manifest.Version), "v")
			runningVersion := strings.TrimPrefix(strings.TrimSpace(version.Version), "v")
			if targetVersion != runningVersion && !version.IsNewer(runningVersion, targetVersion) {
				// RestartAgent is not serialized, so if a prior session died
				// before re-execing into the upgraded binary, re-run the check
				// so the agent still restarts into the new build.
				found = false
			}
		}
	}
	if !found {
		result = execute(ctx, task, e.options)
	}
	// The recorded desired-state hash must be refreshed even when the task
	// itself was already applied earlier: it is what the master compares
	// against to decide the node has converged. Skipping it on the cached
	// path left the master re-dispatching this task on every heartbeat.
	if result.Status == "succeeded" && task.Kind == "nginx.apply_config" {
		if err := saveNginxConfigurationState(e.options.DataDir, task.ExpectedHash); err != nil {
			result = TaskResult{Status: "failed", Summary: "persist Nginx configuration state: " + err.Error()}
		}
	}
	if found {
		return result
	}
	if err := e.saveCompleted(task, result); err != nil {
		return TaskResult{Status: "failed", Summary: "persist task result: " + err.Error()}
	}
	return result
}

func (e *taskExecutor) completed(task Task) (TaskResult, bool, error) {
	path := filepath.Join(e.options.DataDir, "completed-tasks", task.ID+".json")
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return TaskResult{}, false, nil
	}
	if err != nil {
		return TaskResult{}, false, err
	}
	var recorded completedTask
	if err := json.Unmarshal(content, &recorded); err != nil {
		return TaskResult{}, false, err
	}
	if recorded.ID != task.ID || recorded.Kind != task.Kind || recorded.ExpectedHash != task.ExpectedHash {
		return TaskResult{}, false, fmt.Errorf("task ID was reused with a different payload identity")
	}
	return recorded.Result, true, nil
}

func (e *taskExecutor) saveCompleted(task Task, result TaskResult) error {
	directory := filepath.Join(e.options.DataDir, "completed-tasks")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	content, err := json.Marshal(completedTask{ID: task.ID, Kind: task.Kind, ExpectedHash: task.ExpectedHash, Result: result})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".task-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(directory, task.ID+".json"))
}
