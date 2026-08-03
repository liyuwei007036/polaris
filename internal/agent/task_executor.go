package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

var taskIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type taskExecutor struct {
	options TaskOptions
	mu      sync.Mutex
}

type completedTask struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	ExpectedHash string     `json:"expected_hash"`
	Result       TaskResult `json:"result"`
}

func newTaskExecutor(options TaskOptions) *taskExecutor {
	return &taskExecutor{options: options}
}

func (e *taskExecutor) Handle(ctx context.Context, task Task) TaskResult {
	if e.options.DataDir == "" || !taskIDPattern.MatchString(task.ID) {
		return TaskResult{Status: "failed", Summary: "task ID is invalid"}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if result, found, err := e.completed(task); err != nil {
		return TaskResult{Status: "failed", Summary: "read completed task result: " + err.Error()}
	} else if found {
		return result
	}
	result := executeTask(ctx, task, e.options)
	if result.Status == "succeeded" && task.Kind == "nginx.apply_config" {
		if err := saveNginxConfigurationState(e.options.DataDir, task.ExpectedHash); err != nil {
			result = TaskResult{Status: "failed", Summary: "persist Nginx configuration state: " + err.Error()}
		}
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
