package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

// liveTaskTimeout bounds how long a request-response task may take. Reading or
// changing a firewall is a local command on the node, so anything slower than
// this means the node is not answering and the operator should be told so
// rather than left waiting.
const liveTaskTimeout = 20 * time.Second

// ErrNodeOffline reports that a node cannot answer right now. Live state can
// only come from the node itself, so there is nothing to fall back on.
var ErrNodeOffline = userErrorf("服务器当前离线，无法读取或修改它的防护规则")

// AskNode puts a question to a node and waits for that node's own answer,
// instead of queueing an instruction and reporting success before anything
// happened. Every network protection screen is built on this: what it shows is
// what the node reported a moment ago, and what it changes is confirmed by the
// node before the operator is told it worked.
//
// These tasks are deliberately not recorded in the task table. Showing a
// screen asks every server a question, and storing each one would bury the
// operator's actual operations under a stream of reads.
func (s *Server) AskNode(ctx context.Context, nodeID, kind, payload string) (string, error) {
	s.controlMu.Lock()
	session := s.controls[nodeID]
	s.controlMu.Unlock()
	if session == nil {
		return "", ErrNodeOffline
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(payload))
	answer := make(chan wire.TaskResult, 1)
	s.taskWaitMu.Lock()
	s.taskWaiters[id] = answer
	s.taskWaitMu.Unlock()
	defer func() {
		s.taskWaitMu.Lock()
		delete(s.taskWaiters, id)
		s.taskWaitMu.Unlock()
	}()

	waitCtx, cancel := context.WithTimeout(ctx, liveTaskTimeout)
	defer cancel()
	question := wire.Task{ID: id, Kind: kind, IdempotencyKey: id, Payload: payload, ExpectedHash: hex.EncodeToString(digest[:])}
	select {
	case session.questions <- question:
	case <-session.done:
		return "", ErrNodeOffline
	case <-waitCtx.Done():
		return "", userErrorf("服务器正忙，请稍后重试")
	}
	select {
	case result := <-answer:
		if result.Status != "succeeded" {
			summary := result.Summary
			if summary == "" {
				summary = "服务器未能完成这次操作"
			}
			return "", userErrorf("%s", summary)
		}
		return result.Data, nil
	case <-session.done:
		return "", ErrNodeOffline
	case <-waitCtx.Done():
		if errors.Is(ctx.Err(), context.Canceled) {
			return "", ctx.Err()
		}
		return "", userErrorf("服务器在 %d 秒内没有响应", int(liveTaskTimeout/time.Second))
	}
}

// deliverTaskAnswer hands a result to whoever is waiting on it, and reports
// whether anyone was. Stored tasks — every publish and install — have no
// waiter and are recorded as before.
func (s *Server) deliverTaskAnswer(result wire.TaskResult) bool {
	s.taskWaitMu.Lock()
	waiter := s.taskWaiters[result.TaskID]
	s.taskWaitMu.Unlock()
	if waiter == nil {
		return false
	}
	select {
	case waiter <- result:
	default:
	}
	return true
}

// askNodes puts the same question to several nodes at once. A screen covering
// every server would otherwise wait for them one after another, and one slow
// server would hold up all the rest.
func (s *Server) askNodes(ctx context.Context, nodeIDs []string, kind, payload string) map[string]liveAnswer {
	answers := make(map[string]liveAnswer, len(nodeIDs))
	var mutex sync.Mutex
	var group sync.WaitGroup
	for _, nodeID := range nodeIDs {
		group.Add(1)
		go func(nodeID string) {
			defer group.Done()
			data, err := s.AskNode(ctx, nodeID, kind, payload)
			mutex.Lock()
			answers[nodeID] = liveAnswer{data: data, err: err}
			mutex.Unlock()
		}(nodeID)
	}
	group.Wait()
	return answers
}
