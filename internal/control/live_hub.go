package control

import "sync"

type liveEvent struct {
	Kind   string `json:"kind"`
	NodeID string `json:"node_id,omitempty"`
	TaskID string `json:"task_id,omitempty"`
}

type liveHub struct {
	mu      sync.Mutex
	clients map[chan liveEvent]struct{}
}

func newLiveHub() *liveHub {
	return &liveHub{clients: make(map[chan liveEvent]struct{})}
}

func (h *liveHub) publish(event liveEvent) {
	h.mu.Lock()
	clients := make([]chan liveEvent, 0, len(h.clients))
	for client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.Unlock()
	for _, client := range clients {
		select {
		case client <- event:
		default:
		}
	}
}

func (h *liveHub) subscribe() chan liveEvent {
	client := make(chan liveEvent, 16)
	h.mu.Lock()
	h.clients[client] = struct{}{}
	h.mu.Unlock()
	return client
}

func (h *liveHub) unsubscribe(client chan liveEvent) {
	h.mu.Lock()
	delete(h.clients, client)
	h.mu.Unlock()
}
