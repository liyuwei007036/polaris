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

	timeout := liveTaskTimeout
	if kind == "network.probe" || kind == "probe.network" || kind == "probe.tcp" {
		timeout = 45 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
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
		if ctxErr := ctx.Err(); ctxErr != nil {
			if errors.Is(ctxErr, context.Canceled) {
				return "", ctxErr
			}
			// A screen may give the whole read a shorter deadline than one
			// node's live-task budget. Report the deadline the caller set, not
			// the longer per-node timeout.
			return "", userErrorf("服务器未在限定时间内响应")
		}
		return "", userErrorf("服务器在 %d 秒内没有响应", int(timeout/time.Second))
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

// liveAskCall is one live question in flight. Screens that ask the same node
// the same question at the same moment wait on this call instead of sending a
// second one: the security page reads the jail list and the banned list in two
// requests, and both of them need the answer to one fail2ban.query.
type liveAskCall struct {
	done chan struct{}
	data string
	err  error
}

// shareableLiveTask reports whether two identical questions sent at the same
// moment may be answered by one round trip. Only reads qualify; a mutation
// must reach the host once per request.
func shareableLiveTask(kind string) bool {
	switch kind {
	case "firewall.query", "fail2ban.query":
		return true
	default:
		return false
	}
}

// sharedLiveAsk joins an in-flight read or starts it. The call is detached
// from the first request's cancellation: a browser that closes one of the two
// requests must not take the answer away from the other one still waiting.
func (s *Server) sharedLiveAsk(ctx context.Context, nodeID, kind, payload string) *liveAskCall {
	key := nodeID + "\x00" + kind + "\x00" + payload
	s.liveAskMu.Lock()
	if s.liveAsks == nil {
		s.liveAsks = make(map[string]*liveAskCall)
	}
	if call := s.liveAsks[key]; call != nil {
		s.liveAskMu.Unlock()
		return call
	}
	call := &liveAskCall{done: make(chan struct{})}
	s.liveAsks[key] = call
	s.liveAskMu.Unlock()

	go func() {
		call.data, call.err = s.AskNode(context.WithoutCancel(ctx), nodeID, kind, payload)
		close(call.done)
		s.liveAskMu.Lock()
		if s.liveAsks[key] == call {
			delete(s.liveAsks, key)
		}
		s.liveAskMu.Unlock()
	}()
	return call
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
			var data string
			var err error
			if shareableLiveTask(kind) {
				call := s.sharedLiveAsk(ctx, nodeID, kind, payload)
				select {
				case <-call.done:
					data, err = call.data, call.err
				case <-ctx.Done():
					err = ctx.Err()
				}
			} else {
				data, err = s.AskNode(ctx, nodeID, kind, payload)
			}
			mutex.Lock()
			answers[nodeID] = liveAnswer{data: data, err: err}
			mutex.Unlock()
		}(nodeID)
	}
	group.Wait()
	return answers
}
