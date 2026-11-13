package control

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const maxChainResolveHops = 16

type resolvedChainRoute struct {
	IsChained     bool
	NodeIDs       []string
	Path          []string
	FinalNodeID   string
	FinalNodeName string
	FinalOutbound string
	FinalIsDirect bool
	HopResolved   bool
}

type chainEndpointMeta struct {
	NodeID             string
	NodeName           string
	EndpointOutboundID string
	ListenerOutboundID string
}

func isDirectOutbound(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case lower == "", lower == "block":
		return false
	case lower == "direct",
		lower == "direct-out",
		lower == "direct_out",
		lower == "direct outbound",
		lower == "direct-connection",
		lower == "直连",
		strings.HasPrefix(lower, "direct-"),
		strings.HasPrefix(lower, "direct:"),
		strings.HasPrefix(lower, "直连 ("),
		strings.HasPrefix(lower, "直连（"):
		return true
	default:
		return false
	}
}

func connectionChainEndpointID(c storedConnection) string {
	if id := strings.TrimSpace(c.ChainedTarget); id != "" {
		return id
	}
	if id, ok := strings.CutPrefix(strings.TrimSpace(c.Outbound), "chain-"); ok && id != "" {
		return id
	}
	for _, tag := range c.Chains {
		if id, ok := strings.CutPrefix(strings.TrimSpace(tag), "chain-"); ok && id != "" {
			return id
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type chainEndpointMetaCacheEntry struct {
	meta      chainEndpointMeta
	ok        bool
	expiresAt time.Time
}

type nodeDisplayNameCacheEntry struct {
	name      string
	expiresAt time.Time
}

type dbOutboundCacheEntry struct {
	displayName string
	isDirect    bool
	ok          bool
	expiresAt   time.Time
}

var (
	chainResolverCacheMu   sync.RWMutex
	chainEndpointMetaCache = make(map[string]chainEndpointMetaCacheEntry)
	nodeDisplayNameCache   = make(map[string]nodeDisplayNameCacheEntry)
	dbOutboundCache        = make(map[string]dbOutboundCacheEntry)
)

func lookupChainEndpointMeta(ctx context.Context, store *Store, endpointID string) (chainEndpointMeta, bool) {
	epID := strings.TrimSpace(endpointID)
	if store == nil || store.db == nil || epID == "" {
		return chainEndpointMeta{}, false
	}
	now := time.Now()
	chainResolverCacheMu.RLock()
	if entry, found := chainEndpointMetaCache[epID]; found && now.Before(entry.expiresAt) {
		chainResolverCacheMu.RUnlock()
		return entry.meta, entry.ok
	}
	chainResolverCacheMu.RUnlock()

	var meta chainEndpointMeta
	err := store.db.QueryRowContext(ctx, `
		SELECT n.id, n.name, e.outbound_id, l.outbound_id
		FROM endpoints e
		JOIN listeners l ON l.id = e.listener_id
		JOIN nodes n ON n.id = l.node_id
		WHERE e.id = ? AND n.revoked_at IS NULL`,
		epID,
	).Scan(&meta.NodeID, &meta.NodeName, &meta.EndpointOutboundID, &meta.ListenerOutboundID)
	ok := (err == nil)

	chainResolverCacheMu.Lock()
	chainEndpointMetaCache[epID] = chainEndpointMetaCacheEntry{
		meta:      meta,
		ok:        ok,
		expiresAt: now.Add(30 * time.Second),
	}
	chainResolverCacheMu.Unlock()
	return meta, ok
}

func lookupNodeDisplayName(ctx context.Context, store *Store, cached map[string]string, nodeID, fallback string) string {
	if name := strings.TrimSpace(cached[nodeID]); name != "" {
		return name
	}
	trimmedID := strings.TrimSpace(nodeID)
	if store != nil && trimmedID != "" {
		now := time.Now()
		chainResolverCacheMu.RLock()
		if entry, found := nodeDisplayNameCache[trimmedID]; found && now.Before(entry.expiresAt) {
			chainResolverCacheMu.RUnlock()
			if entry.name != "" {
				return entry.name
			}
		} else {
			chainResolverCacheMu.RUnlock()
		}

		if node, err := store.NodeByID(ctx, trimmedID); err == nil && strings.TrimSpace(node.Name) != "" {
			name := strings.TrimSpace(node.Name)
			chainResolverCacheMu.Lock()
			nodeDisplayNameCache[trimmedID] = nodeDisplayNameCacheEntry{
				name:      name,
				expiresAt: now.Add(30 * time.Second),
			}
			chainResolverCacheMu.Unlock()
			return name
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	return nodeID
}

func resolveDBOutbound(ctx context.Context, store *Store, rawOutboundID string) (displayName string, isDirect bool, ok bool) {
	id := strings.TrimSpace(rawOutboundID)
	switch {
	case id == "":
		return "直连", true, true
	case id == "direct":
		return "直连", true, true
	case strings.HasPrefix(id, DirectOutboundPrefix):
		if ip := DirectOutboundIP(id); ip != "" {
			return fmt.Sprintf("直连 (%s)", ip), true, true
		}
		return "直连", true, true
	}
	if store == nil || store.db == nil {
		return id, isDirectOutbound(id), false
	}

	now := time.Now()
	chainResolverCacheMu.RLock()
	if entry, found := dbOutboundCache[id]; found && now.Before(entry.expiresAt) {
		chainResolverCacheMu.RUnlock()
		return entry.displayName, entry.isDirect, entry.ok
	}
	chainResolverCacheMu.RUnlock()

	var name, obType string
	if err := store.db.QueryRowContext(ctx, `SELECT name, type FROM outbounds WHERE id = ?`, id).Scan(&name, &obType); err == nil {
		isDirect = strings.EqualFold(strings.TrimSpace(obType), "direct") || isDirectOutbound(name)
		resName := firstNonEmpty(name, id)
		chainResolverCacheMu.Lock()
		dbOutboundCache[id] = dbOutboundCacheEntry{
			displayName: resName,
			isDirect:    isDirect,
			ok:          true,
			expiresAt:   now.Add(30 * time.Second),
		}
		chainResolverCacheMu.Unlock()
		return resName, isDirect, true
	}

	chainResolverCacheMu.Lock()
	dbOutboundCache[id] = dbOutboundCacheEntry{
		displayName: id,
		isDirect:    isDirectOutbound(id),
		ok:          false,
		expiresAt:   now.Add(30 * time.Second),
	}
	chainResolverCacheMu.Unlock()
	return id, isDirectOutbound(id), false
}

func destinationHostPort(raw string) (host, port string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ""
	}
	if h, p, err := net.SplitHostPort(trimmed); err == nil {
		host = h
		port = strings.TrimSpace(p)
	} else {
		host = trimmed
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if parsed := net.ParseIP(host); parsed != nil {
		host = parsed.String()
	}
	return host, port
}

func connectionDestMatches(a, b storedConnection) bool {
	if a.Network != "" && b.Network != "" && !strings.EqualFold(a.Network, b.Network) {
		return false
	}
	aDestHost, aPort := destinationHostPort(a.Destination)
	bDestHost, bPort := destinationHostPort(b.Destination)
	if aPort != "" && bPort != "" && aPort != bPort {
		return false
	}

	aHost := strings.TrimSpace(a.Host)
	bHost := strings.TrimSpace(b.Host)
	if aHost != "" && bHost != "" {
		return strings.EqualFold(aHost, bHost)
	}

	if aDestHost != "" && bDestHost != "" {
		if parsedA, parsedB := net.ParseIP(aDestHost), net.ParseIP(bDestHost); parsedA != nil && parsedB != nil {
			if parsedA.Equal(parsedB) && (aPort == bPort || aPort == "" || bPort == "") {
				return true
			}
		} else if strings.EqualFold(aDestHost, bDestHost) && (aPort == bPort || aPort == "" || bPort == "") {
			return true
		}
	}

	// Support matching a literal host string against the other side's Destination host.
	if aHost != "" && bDestHost != "" && strings.EqualFold(aHost, bDestHost) {
		return true
	}
	if bHost != "" && aDestHost != "" && strings.EqualFold(bHost, aDestHost) {
		return true
	}
	return false
}

func matchDownstreamRelayConn(active map[string][]storedConnection, fromNodeID, preferredTargetNodeID string, entryConn storedConnection, visitedNodes map[string]bool) (string, storedConnection, bool) {
	return matchDownstreamRelayConnScored(active, nil, fromNodeID, preferredTargetNodeID, entryConn, visitedNodes, 0, nil)
}

// resolveChainRoute traces a connection through live downstream relay legs and,
// when no live relay leg is present, DB chain configuration to determine the
// full node path and true final outbound.
func resolveChainRoute(
	ctx context.Context,
	store *Store,
	hub *connectionsHub,
	fallback map[string][]storedConnection,
	cachedNodeNames map[string]string,
	nodeID, nodeName string,
	conn storedConnection,
) resolvedChainRoute {
	if connectionChainEndpointID(conn) == "" {
		return resolveChainRouteWithActive(ctx, store, hub, nil, cachedNodeNames, nodeID, nodeName, conn)
	}
	active := make(map[string][]storedConnection, len(fallback))
	for k, v := range fallback {
		active[k] = v
	}
	if hub != nil {
		for k, v := range hub.activeNodeConnections() {
			active[k] = v
		}
	}
	return resolveChainRouteWithActive(ctx, store, hub, active, cachedNodeNames, nodeID, nodeName, conn)
}

func resolveChainRouteWithActive(
	ctx context.Context,
	store *Store,
	hub *connectionsHub,
	active map[string][]storedConnection,
	cachedNodeNames map[string]string,
	nodeID, nodeName string,
	conn storedConnection,
) resolvedChainRoute {
	entryName := lookupNodeDisplayName(ctx, store, cachedNodeNames, nodeID, nodeName)
	epID := connectionChainEndpointID(conn)
	if epID == "" {
		finalOut := firstNonEmpty(conn.OutboundName, conn.Outbound)
		finalDirect := conn.OutboundDirect || isDirectOutbound(conn.Outbound) || isDirectOutbound(finalOut)
		return resolvedChainRoute{
			IsChained:     false,
			NodeIDs:       []string{nodeID},
			Path:          []string{entryName},
			FinalNodeID:   nodeID,
			FinalNodeName: entryName,
			FinalOutbound: finalOut,
			FinalIsDirect: finalDirect,
			HopResolved:   true,
		}
	}

	metaCache := make(map[string]chainEndpointMeta)
	hasMetaCache := make(map[string]bool)
	getEndpointMeta := func(targetEP string) (chainEndpointMeta, bool) {
		if targetEP == "" {
			return chainEndpointMeta{}, false
		}
		if ok, cached := hasMetaCache[targetEP]; cached {
			return metaCache[targetEP], ok
		}
		meta, ok := lookupChainEndpointMeta(ctx, store, targetEP)
		metaCache[targetEP] = meta
		hasMetaCache[targetEP] = ok
		return meta, ok
	}
	preferredTargetsOnNode := func(curNode string) map[string]string {
		var out map[string]string
		for _, c := range active[curNode] {
			sibEP := connectionChainEndpointID(c)
			if sibEP == "" {
				continue
			}
			if meta, ok := getEndpointMeta(sibEP); ok && meta.NodeID != "" {
				if out == nil {
					out = make(map[string]string)
				}
				out[sibEP] = meta.NodeID
			}
		}
		return out
	}

	nodeIDs := []string{nodeID}
	path := []string{entryName}
	visitedNodes := map[string]bool{nodeID: true}
	visitedEndpoints := map[string]bool{}
	currentNodeID := nodeID
	currentConn := conn

	for hop := 0; hop < maxChainResolveHops && epID != ""; hop++ {
		if visitedEndpoints[epID] {
			break
		}
		visitedEndpoints[epID] = true

		meta, hasMeta := getEndpointMeta(epID)
		preferredNodeID := ""
		if hasMeta {
			preferredNodeID = meta.NodeID
		}

		if nextNodeID, nextConn, matched := matchDownstreamRelayConnScored(active, hub, currentNodeID, preferredNodeID, currentConn, visitedNodes, hop, preferredTargetsOnNode(currentNodeID)); matched {
			nextName := lookupNodeDisplayName(ctx, store, cachedNodeNames, nextNodeID, meta.NodeName)
			nodeIDs = append(nodeIDs, nextNodeID)
			path = append(path, nextName)
			visitedNodes[nextNodeID] = true

			nextEP := connectionChainEndpointID(nextConn)
			if nextEP != "" {
				currentNodeID = nextNodeID
				currentConn = nextConn
				epID = nextEP
				continue
			}

			finalOut := firstNonEmpty(nextConn.OutboundName, nextConn.Outbound)
			finalDirect := nextConn.OutboundDirect || isDirectOutbound(nextConn.Outbound) || isDirectOutbound(finalOut)
			return resolvedChainRoute{
				IsChained:     true,
				NodeIDs:       nodeIDs,
				Path:          path,
				FinalNodeID:   nextNodeID,
				FinalNodeName: nextName,
				FinalOutbound: finalOut,
				FinalIsDirect: finalDirect,
				HopResolved:   true,
			}
		}

		// No active downstream relay leg matched right now; trace static DB chain topology.
		if hasMeta && !visitedNodes[meta.NodeID] {
			nextName := lookupNodeDisplayName(ctx, store, cachedNodeNames, meta.NodeID, meta.NodeName)
			nodeIDs = append(nodeIDs, meta.NodeID)
			path = append(path, nextName)
			visitedNodes[meta.NodeID] = true

			effOB := firstNonEmpty(meta.EndpointOutboundID, meta.ListenerOutboundID)
			if nextEP, ok := strings.CutPrefix(effOB, ChainOutboundPrefix); ok && strings.TrimSpace(nextEP) != "" {
				currentNodeID = meta.NodeID
				epID = strings.TrimSpace(nextEP)
				continue
			}

			finalOut, finalDirect, resolvedOB := resolveDBOutbound(ctx, store, effOB)
			return resolvedChainRoute{
				IsChained:     true,
				NodeIDs:       nodeIDs,
				Path:          path,
				FinalNodeID:   meta.NodeID,
				FinalNodeName: nextName,
				FinalOutbound: finalOut,
				FinalIsDirect: finalDirect,
				HopResolved:   resolvedOB,
			}
		}
		break
	}

	fallbackOut := firstNonEmpty(conn.OutboundName, conn.Outbound)
	return resolvedChainRoute{
		IsChained:     true,
		NodeIDs:       nodeIDs,
		Path:          path,
		FinalNodeID:   currentNodeID,
		FinalNodeName: path[len(path)-1],
		FinalOutbound: fallbackOut,
		FinalIsDirect: isDirectOutbound(fallbackOut),
		HopResolved:   false,
	}
}

func findOriginEntryConn(
	active map[string][]storedConnection,
	hub *connectionsHub,
	relayNodeID string,
	relayConn storedConnection,
) (entryConn storedConnection, ok bool) {
	if len(active) == 0 {
		return storedConnection{}, false
	}
	curNodeID := relayNodeID
	cur := relayConn
	var latestMatch storedConnection
	visited := map[string]bool{}
	for hop := 0; hop < maxChainResolveHops; hop++ {
		upstreamID := strings.TrimSpace(cur.RelayUpstreamNodeID)
		if upstreamID == "" || visited[upstreamID] {
			break
		}
		visited[upstreamID] = true
		matchedConn, matched := matchOriginEntryScored(active, hub, upstreamID, curNodeID, cur)
		if !matched {
			break
		}
		latestMatch = matchedConn
		curNodeID = upstreamID
		cur = matchedConn
	}
	return latestMatch, latestMatch.Destination != "" || latestMatch.Host != "" || latestMatch.User != "" || latestMatch.SourceIP != ""
}

// enrichRelayConnections resolves upstream node names and entry client details
// for IsChainRelay connections when reporting relay/landing node telemetry.
func enrichRelayConnections(
	ctx context.Context,
	store *Store,
	hub *connectionsHub,
	fallback map[string][]storedConnection,
	cachedNodeNames map[string]string,
	connections []storedConnection,
) []storedConnection {
	active := make(map[string][]storedConnection, len(fallback))
	for k, v := range fallback {
		active[k] = v
	}
	if hub != nil {
		for k, v := range hub.activeNodeConnections() {
			active[k] = v
		}
	}

	activeNodeByConnPtr := func(c storedConnection) string {
		for nid, list := range active {
			for _, item := range list {
				if item.ID != "" && item.ID == c.ID && item.RelayUpstreamNodeID == c.RelayUpstreamNodeID {
					return nid
				}
			}
		}
		return ""
	}

	out := make([]storedConnection, len(connections))
	for i, c := range connections {
		enriched := c
		if c.RelayUpstreamNodeID != "" {
			curNodeID := activeNodeByConnPtr(c)
			if entry, found := findOriginEntryConn(active, hub, curNodeID, c); found {
				if entry.User != "" && !isInternalUserID(entry.User) {
					enriched.User = entry.User
				}
				if cleanIPAddress(entry.SourceIP) != "" {
					enriched.SourceIP = entry.SourceIP
					enriched.Source = entry.Source
					enriched.SourceLocation = entry.SourceLocation
				}
				if entry.ListenerName != "" {
					enriched.ListenerName = entry.ListenerName
				}
				if enriched.Host == "" && entry.Host != "" {
					enriched.Host = entry.Host
				}
				if enriched.Destination == "" && entry.Destination != "" {
					enriched.Destination = entry.Destination
				}
			}
			if name := lookupNodeDisplayName(ctx, store, cachedNodeNames, c.RelayUpstreamNodeID, ""); name != "" {
				enriched.RelayUpstreamNodeID = name
			}
		}
		out[i] = enriched
	}
	return out
}
