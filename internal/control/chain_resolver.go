package control

import (
	"context"
	"fmt"
	"net"
	"strings"
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

func lookupChainEndpointMeta(ctx context.Context, store *Store, endpointID string) (chainEndpointMeta, bool) {
	if store == nil || store.db == nil || strings.TrimSpace(endpointID) == "" {
		return chainEndpointMeta{}, false
	}
	var meta chainEndpointMeta
	err := store.db.QueryRowContext(ctx, `
		SELECT n.id, n.name, e.outbound_id, l.outbound_id
		FROM endpoints e
		JOIN listeners l ON l.id = e.listener_id
		JOIN nodes n ON n.id = l.node_id
		WHERE e.id = ? AND n.revoked_at IS NULL`,
		endpointID,
	).Scan(&meta.NodeID, &meta.NodeName, &meta.EndpointOutboundID, &meta.ListenerOutboundID)
	if err != nil {
		return chainEndpointMeta{}, false
	}
	return meta, true
}

func lookupNodeDisplayName(ctx context.Context, store *Store, cached map[string]string, nodeID, fallback string) string {
	if name := strings.TrimSpace(cached[nodeID]); name != "" {
		return name
	}
	if store != nil && nodeID != "" {
		if node, err := store.NodeByID(ctx, nodeID); err == nil && strings.TrimSpace(node.Name) != "" {
			return strings.TrimSpace(node.Name)
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
	var name, obType string
	if err := store.db.QueryRowContext(ctx, `SELECT name, type FROM outbounds WHERE id = ?`, id).Scan(&name, &obType); err == nil {
		isDirect = strings.EqualFold(strings.TrimSpace(obType), "direct") || isDirectOutbound(name)
		return firstNonEmpty(name, id), isDirect, true
	}
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
	if len(active) == 0 {
		return "", storedConnection{}, false
	}
	findInNode := func(candNodeID string, strict bool) (storedConnection, bool) {
		for _, oc := range active[candNodeID] {
			if !oc.IsChainRelay {
				if strict || oc.RelayUpstreamNodeID != fromNodeID {
					if strict || !oc.OnChainTarget || oc.RelayUpstreamNodeID != "" || preferredTargetNodeID != candNodeID {
						continue
					}
				}
			}
			if oc.RelayUpstreamNodeID != "" && oc.RelayUpstreamNodeID != fromNodeID {
				continue
			}
			if strict && oc.RelayUpstreamNodeID == "" && preferredTargetNodeID != candNodeID {
				continue
			}
			if connectionDestMatches(entryConn, oc) {
				return oc, true
			}
		}
		return storedConnection{}, false
	}

	if preferredTargetNodeID != "" && !visitedNodes[preferredTargetNodeID] {
		if oc, ok := findInNode(preferredTargetNodeID, true); ok {
			return preferredTargetNodeID, oc, true
		}
		if oc, ok := findInNode(preferredTargetNodeID, false); ok {
			return preferredTargetNodeID, oc, true
		}
	}

	// Fall back across all nodes when RelayUpstreamNodeID identifies the hop.
	for candNodeID := range active {
		if candNodeID == fromNodeID || visitedNodes[candNodeID] {
			continue
		}
		for _, oc := range active[candNodeID] {
			if !oc.IsChainRelay || oc.RelayUpstreamNodeID != fromNodeID {
				continue
			}
			if connectionDestMatches(entryConn, oc) {
				return candNodeID, oc, true
			}
		}
	}
	return "", storedConnection{}, false
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

	active := make(map[string][]storedConnection, len(fallback))
	for k, v := range fallback {
		active[k] = v
	}
	if hub != nil {
		for k, v := range hub.activeNodeConnections() {
			active[k] = v
		}
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

		meta, hasMeta := lookupChainEndpointMeta(ctx, store, epID)
		preferredNodeID := ""
		if hasMeta {
			preferredNodeID = meta.NodeID
		}

		if nextNodeID, nextConn, matched := matchDownstreamRelayConn(active, currentNodeID, preferredNodeID, currentConn, visitedNodes); matched {
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
	relayConn storedConnection,
) (entryConn storedConnection, ok bool) {
	if len(active) == 0 {
		return storedConnection{}, false
	}
	cur := relayConn
	var latestMatch storedConnection
	visited := map[string]bool{}
	for hop := 0; hop < maxChainResolveHops; hop++ {
		upstreamID := strings.TrimSpace(cur.RelayUpstreamNodeID)
		if upstreamID == "" || visited[upstreamID] {
			break
		}
		visited[upstreamID] = true
		matched := false
		for _, cand := range active[upstreamID] {
			if connectionChainEndpointID(cand) == "" {
				continue
			}
			if connectionDestMatches(cand, cur) {
				latestMatch = cand
				cur = cand
				matched = true
				break
			}
		}
		if !matched {
			break
		}
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

	out := make([]storedConnection, len(connections))
	for i, c := range connections {
		enriched := c
		if c.RelayUpstreamNodeID != "" {
			if entry, found := findOriginEntryConn(active, c); found {
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
