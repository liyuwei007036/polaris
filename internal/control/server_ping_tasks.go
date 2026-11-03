package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

var errInvalidPingRequest = errors.New("invalid request")

func (s *Server) listPingTasks(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	tasks, err := s.store.ListPingTasks(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) savePingTask(w http.ResponseWriter, r *http.Request) {
	op, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var task PingTask
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		writeError(w, errInvalidPingRequest)
		return
	}
	if err := s.store.SavePingTask(r.Context(), &task); err != nil {
		writeError(w, err)
		return
	}
	_ = s.reconcilePingTasks(r.Context(), op.ID)
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) updatePingTask(w http.ResponseWriter, r *http.Request) {
	op, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, errInvalidPingRequest)
		return
	}
	var task PingTask
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		writeError(w, errInvalidPingRequest)
		return
	}
	task.ID = id
	if err := s.store.SavePingTask(r.Context(), &task); err != nil {
		writeError(w, err)
		return
	}
	_ = s.reconcilePingTasks(r.Context(), op.ID)
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) deletePingTask(w http.ResponseWriter, r *http.Request) {
	op, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, errInvalidPingRequest)
		return
	}
	if err := s.store.DeletePingTask(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	_ = s.reconcilePingTasks(r.Context(), op.ID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) reorderPingTasks(w http.ResponseWriter, r *http.Request) {
	op, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, errInvalidPingRequest)
		return
	}
	if err := s.store.ReorderPingTasks(r.Context(), req.IDs); err != nil {
		writeError(w, err)
		return
	}
	_ = s.reconcilePingTasks(r.Context(), op.ID)
	writeJSON(w, http.StatusOK, map[string]any{"reordered": true})
}

// reconcilePingTasks turns the ref model (one selected probe set per node) into
// durable agent tasks. The master never probes; it only sends the current set.
func (s *Server) reconcilePingTasks(ctx context.Context, operatorID string) error {
	tasks, err := s.store.ListPingTasks(ctx)
	if err != nil {
		return err
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if err := s.reconcilePingTasksNode(ctx, node.ID, tasks, operatorID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) reconcilePingTasksNode(ctx context.Context, nodeID string, tasks []PingTask, operatorID string) error {
	selected := make([]PingTask, 0, len(tasks))
	for _, task := range tasks {
		if task.AutoJoin || containsString(task.NodeIDs, nodeID) {
			selected = append(selected, task)
		}
	}
	payload := make([]map[string]string, 0, len(selected))
	for _, task := range selected {
		payload = append(payload, map[string]string{"id": strconv.FormatInt(task.ID, 10), "name": task.Name, "target": task.Target})
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	_, err := s.DispatchTask(ctx, Task{NodeID: nodeID, OperatorID: operatorID, Kind: "probe.configure", IdempotencyKey: "probe-config-" + nodeID + "-" + fmt.Sprintf("%x", digest[:8]), Payload: string(encoded), ExpectedHash: fmt.Sprintf("%x", digest[:])})
	return err
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == wanted {
			return true
		}
	}
	return false
}
