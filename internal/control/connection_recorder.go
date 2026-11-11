package control

import (
	"context"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

type trackedConnection struct {
	record   ConnectionRecord
	lastSeen time.Time
}

// connectionRecorder tracks the lifecycle of connections from agents,
// buffers updates, and writes them to the SQLite store in efficient batches.
type connectionRecorder struct {
	mu             sync.Mutex
	store          *Store
	locator        *ipLocator
	hub            *connectionsHub
	active         map[string]*trackedConnection  // key: nodeID + ":" + connID
	nodeConns      map[string]map[string]struct{} // nodeID -> set of connID
	pendingOnChain map[string]time.Time           // key -> first seen when OnChainTarget=true
	dirty          map[string]ConnectionRecord    // key -> record to save
	stopCh         chan struct{}
	doneCh         chan struct{}
	flushTicker    *time.Ticker
	batchTrigger   chan struct{}
}

func newConnectionRecorder(store *Store, locator ...*ipLocator) *connectionRecorder {
	var loc *ipLocator
	if len(locator) > 0 {
		loc = locator[0]
	}
	return &connectionRecorder{
		store:          store,
		locator:        loc,
		active:         make(map[string]*trackedConnection),
		nodeConns:      make(map[string]map[string]struct{}),
		pendingOnChain: make(map[string]time.Time),
		dirty:          make(map[string]ConnectionRecord),
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
		batchTrigger:   make(chan struct{}, 1),
	}
}

func (r *connectionRecorder) SetIPLocator(locator *ipLocator) {
	r.mu.Lock()
	r.locator = locator
	r.mu.Unlock()
}

func (r *connectionRecorder) SetConnectionsHub(hub *connectionsHub) {
	r.mu.Lock()
	r.hub = hub
	r.mu.Unlock()
}

func (r *connectionRecorder) Start(ctx context.Context) {
	r.flushTicker = time.NewTicker(3 * time.Second)
	go r.run(ctx)
}

func (r *connectionRecorder) Stop() {
	r.mu.Lock()
	if r.flushTicker != nil {
		r.flushTicker.Stop()
	}
	select {
	case <-r.stopCh:
	default:
		close(r.stopCh)
	}
	r.mu.Unlock()

	<-r.doneCh
	// Final flush on stop
	r.Flush(context.Background())
}

func (r *connectionRecorder) run(ctx context.Context) {
	defer close(r.doneCh)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-r.batchTrigger:
			r.Flush(ctx)
		case <-r.flushTicker.C:
			r.Flush(ctx)
		}
	}
}

func (r *connectionRecorder) resolveOutboundName(nodeID, nodeName string, c storedConnection) string {
	outboundName := c.OutboundName
	if connectionChainEndpointID(c) != "" {
		resolved := resolveChainRoute(context.Background(), r.store, r.hub, nil, nil, nodeID, nodeName, c)
		if resolved.HopResolved && resolved.FinalOutbound != "" {
			if len(resolved.Path) > 1 {
				outboundName = strings.Join(resolved.Path[1:], " → ") + " → " + resolved.FinalOutbound
			} else {
				outboundName = resolved.FinalOutbound
			}
		}
	}
	return outboundName
}

// RecordPush processes a connection snapshot pushed from an agent node.
func (r *connectionRecorder) RecordPush(nodeID, nodeName string, connections []storedConnection) {
	if r.store == nil {
		return
	}
	now := time.Now()
	nowRFC := now.UTC().Format(time.RFC3339)

	r.mu.Lock()
	defer r.mu.Unlock()

	currentConnIDs := make(map[string]struct{}, len(connections))
	prevConnIDs := r.nodeConns[nodeID]
	if prevConnIDs == nil {
		prevConnIDs = make(map[string]struct{})
		r.nodeConns[nodeID] = prevConnIDs
	}

	for _, c := range connections {
		if c.ID == "" {
			continue
		}
		key := nodeID + ":" + c.ID
		if c.IsChainRelay {
			delete(r.pendingOnChain, key)
			delete(r.dirty, key)
			delete(r.active, key)
			continue
		}
		if c.OnChainTarget {
			firstSeen, ok := r.pendingOnChain[key]
			if !ok {
				r.pendingOnChain[key] = now
				currentConnIDs[c.ID] = struct{}{}
				continue
			}
			if now.Sub(firstSeen) < chainRelayConfirmWait {
				currentConnIDs[c.ID] = struct{}{}
				continue
			}
			delete(r.pendingOnChain, key)
		} else {
			delete(r.pendingOnChain, key)
		}

		currentConnIDs[c.ID] = struct{}{}

		tracked, exists := r.active[key]
		if !exists {
			// Extract port from Source if present
			sourcePort := ""
			if _, port, err := net.SplitHostPort(c.Source); err == nil {
				sourcePort = port
			}

			startedAt := c.StartedAt
			if startedAt == "" {
				startedAt = nowRFC
			}

			destIP := addressIP(c.Destination)
			if destIP == "" && c.Host != "" && net.ParseIP(c.Host) != nil {
				destIP = c.Host
			}
			destLocation := c.DestinationLocation
			if destLocation == "" && destIP != "" && r.locator != nil {
				destLocation = r.locator.Locate(destIP)
			}

			outboundName := r.resolveOutboundName(nodeID, nodeName, c)

			rec := ConnectionRecord{
				ID:                  key,
				NodeID:              nodeID,
				NodeName:            nodeName,
				ConnectionID:        c.ID,
				SourceIP:            c.SourceIP,
				SourcePort:          sourcePort,
				SourceLocation:      c.SourceLocation,
				Destination:         c.Destination,
				DestinationLocation: destLocation,
				Host:                c.Host,
				Network:             strings.ToLower(c.Network),
				User:                c.User,
				ListenerName:        c.ListenerName,
				OutboundName:        outboundName,
				Upload:              c.Upload,
				Download:            c.Download,
				StartedAt:           startedAt,
			}
			r.active[key] = &trackedConnection{record: rec, lastSeen: now}
			r.dirty[key] = rec
		} else {
			changed := false
			if c.Upload > tracked.record.Upload || c.Download > tracked.record.Download {
				tracked.record.Upload = c.Upload
				tracked.record.Download = c.Download
				tracked.lastSeen = now
				changed = true
			}
			if connectionChainEndpointID(c) != "" {
				if updatedOutbound := r.resolveOutboundName(nodeID, nodeName, c); updatedOutbound != "" && updatedOutbound != tracked.record.OutboundName {
					tracked.record.OutboundName = updatedOutbound
					changed = true
				}
			}
			if changed {
				r.dirty[key] = tracked.record
			}
		}
	}

	// Detect closed connections that were active previously for this node but missing in current push
	for connID := range prevConnIDs {
		if _, stillActive := currentConnIDs[connID]; !stillActive {
			key := nodeID + ":" + connID
			delete(r.pendingOnChain, key)
			if tracked, ok := r.active[key]; ok {
				tracked.record.ClosedAt = nowRFC
				r.dirty[key] = tracked.record
				delete(r.active, key)
			}
		}
	}

	r.nodeConns[nodeID] = currentConnIDs

	// If dirty batch is large, trigger an immediate async flush
	if len(r.dirty) >= 100 {
		select {
		case r.batchTrigger <- struct{}{}:
		default:
		}
	}
}

// Flush writes all pending dirty connection records to SQLite.
func (r *connectionRecorder) Flush(ctx context.Context) {
	r.mu.Lock()
	if len(r.dirty) == 0 {
		r.mu.Unlock()
		return
	}
	batch := make([]ConnectionRecord, 0, len(r.dirty))
	for _, rec := range r.dirty {
		batch = append(batch, rec)
	}
	r.dirty = make(map[string]ConnectionRecord)
	r.mu.Unlock()

	if err := r.store.SaveConnectionRecords(ctx, batch); err != nil {
		log.Printf("flush connection records batch (%d): %v", len(batch), err)
	}
}
