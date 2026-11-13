package control

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	clockDriftDeadband       = 3 * time.Second
	maxChainMatcherHops      = 16
	stickyPairBonus          = 200.0
	stickyDivergeMinBytes    = 64 * 1024
	stickyDivergeRatioThresh = 0.05
)

type stickyChainPair struct {
	upstreamNodeID string
	upstreamConnID string
	targetNodeID   string
	targetConnID   string
	lastSeen       time.Time
}

func stickyChainKey(upstreamNodeID, upstreamConnID string, hopIndex int) string {
	return upstreamNodeID + "\x00" + upstreamConnID + "\x00" + strconv.Itoa(hopIndex)
}

func parseConnStartedAt(raw string) (time.Time, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
		return parsed, true
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

func normalizedConnStart(conn storedConnection, nodeID string, clockOffsets map[string]time.Duration) (time.Time, bool) {
	parsed, ok := parseConnStartedAt(conn.StartedAt)
	if !ok {
		return time.Time{}, false
	}
	if len(clockOffsets) > 0 && nodeID != "" {
		if offset, exists := clockOffsets[nodeID]; exists {
			return parsed.Add(-offset), true
		}
	}
	return parsed, true
}

// relayEvidenceTier evaluates whether candidate cand on candNodeID is an eligible
// downstream hop for an entry connection on fromNodeID (with optional preferredTargetNodeID),
// returning 0 when ineligible or a positive tier strength in [1..4].
func relayEvidenceTier(fromNodeID, preferredTargetNodeID, candNodeID string, cand storedConnection) int {
	if fromNodeID == "" || candNodeID == "" || candNodeID == fromNodeID {
		return 0
	}
	if cand.IsChainRelay {
		if cand.RelayUpstreamNodeID != "" && cand.RelayUpstreamNodeID != fromNodeID {
			return 0
		}
		if cand.RelayUpstreamNodeID == fromNodeID {
			if preferredTargetNodeID != "" && candNodeID == preferredTargetNodeID {
				return 4
			}
			return 3
		}
		if preferredTargetNodeID != "" && candNodeID == preferredTargetNodeID {
			return 2
		}
		return 0
	}
	if preferredTargetNodeID != "" && candNodeID == preferredTargetNodeID && cand.OnChainTarget && cand.RelayUpstreamNodeID == "" {
		return 1
	}
	return 0
}

func ratioSimilarity(a, b float64) float64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > b {
		return b / a
	}
	return a / b
}

// scoreChainCandidate scores how well an upstream leg and a candidate downstream
// leg correspond using evidence tier, destination specificity, clock-normalized
// start time, transferred bytes, live rates, and sticky lock state.
func scoreChainCandidate(
	fromNodeID, candNodeID string,
	entry, cand storedConnection,
	evidence int,
	clockOffsets map[string]time.Duration,
	isSticky bool,
) (score float64, absTimeDeltaMs float64) {
	score = float64(evidence) * 1000.0
	absTimeDeltaMs = math.MaxFloat64

	if entry.Host != "" && cand.Host != "" && strings.EqualFold(strings.TrimSpace(entry.Host), strings.TrimSpace(cand.Host)) {
		score += 20
	}
	_, aPort := destinationHostPort(entry.Destination)
	_, bPort := destinationHostPort(cand.Destination)
	if aPort != "" && bPort != "" && aPort == bPort {
		score += 10
	}

	if entryStart, okE := normalizedConnStart(entry, fromNodeID, clockOffsets); okE {
		if candStart, okC := normalizedConnStart(cand, candNodeID, clockOffsets); okC {
			diff := candStart.Sub(entryStart)
			diffSec := diff.Seconds()
			absTimeDeltaMs = math.Abs(diffSec * 1000.0)
			switch {
			case diff >= 0 && diff <= 2*time.Second:
				score += 120 - math.Min(diffSec*2.0, 4.0)
			case diff >= -2*time.Second && diff < 0:
				score += 90 - math.Min((-diffSec)*5.0, 10.0)
			case diff > 2*time.Second && diff <= 10*time.Second:
				score += 50 - math.Min((diffSec-2.0)*2.0, 16.0)
			case diff > 10*time.Second && diff <= 60*time.Second:
				score += 10 - math.Min((diffSec-10.0)*0.2, 10.0)
			case diff >= -10*time.Second && diff < -2*time.Second:
				score -= 20 + math.Min((-diffSec-2.0)*2.5, 20.0)
			default:
				score -= 150 + math.Min(math.Abs(diffSec)*0.1, 100.0)
			}
		}
	}

	upTot := entry.Upload + entry.Download
	downTot := cand.Upload + cand.Download
	maxTot := upTot
	if downTot > maxTot {
		maxTot = downTot
	}
	totRatio := 1.0
	if maxTot >= 2048 {
		totRatio = ratioSimilarity(float64(upTot), float64(downTot))
		switch {
		case totRatio >= 0.85:
			score += 100
		case totRatio >= 0.60:
			score += 60
		case totRatio >= 0.30:
			score += 15
		case totRatio < 0.10:
			score -= 140
		default:
			score -= 40
		}
		if maxInt64(entry.Upload, cand.Upload) >= 2048 {
			r := ratioSimilarity(float64(entry.Upload), float64(cand.Upload))
			if r >= 0.70 {
				score += 25
			} else if r < 0.10 {
				score -= 20
			}
		}
		if maxInt64(entry.Download, cand.Download) >= 2048 {
			r := ratioSimilarity(float64(entry.Download), float64(cand.Download))
			if r >= 0.70 {
				score += 25
			} else if r < 0.10 {
				score -= 20
			}
		}
	}

	if entry.HasRates && cand.HasRates {
		upRate := entry.UploadRate + entry.DownloadRate
		downRate := cand.UploadRate + cand.DownloadRate
		maxRate := math.Max(upRate, downRate)
		if maxRate >= 512 {
			rr := ratioSimilarity(upRate, downRate)
			switch {
			case rr >= 0.70:
				score += 40
			case rr >= 0.40:
				score += 15
			case rr < 0.10:
				score -= 40
			}
		}
	}

	if isSticky && !(maxTot >= stickyDivergeMinBytes && totRatio < stickyDivergeRatioThresh) {
		score += stickyPairBonus
	}
	return score, absTimeDeltaMs
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

type scoredChainEdge struct {
	entryKey       string
	candNodeID     string
	candKey        string
	candConn       storedConnection
	score          float64
	absTimeDeltaMs float64
}

// matchDownstreamRelayConnScored resolves the best downstream relay connection for
// entryConn across active node snapshots using 1-to-1 global edge assignment among
// sibling connections on fromNodeID, multi-dimensional scoring, and hub sticky locking.
func matchDownstreamRelayConnScored(
	active map[string][]storedConnection,
	hub *connectionsHub,
	fromNodeID, preferredTargetNodeID string,
	entryConn storedConnection,
	visitedNodes map[string]bool,
	hopIndex int,
	preferredTargetByEndpoint map[string]string,
) (string, storedConnection, bool) {
	if len(active) == 0 || fromNodeID == "" {
		return "", storedConnection{}, false
	}
	var clockOffsets map[string]time.Duration
	if hub != nil {
		clockOffsets = hub.clockOffsetsSnapshot()
	}

	// Fast-path: if this connection already established a sticky pair with a downstream relay
	// leg in a previous round and that leg is still alive and matching destination, reuse it directly.
	if hub != nil && entryConn.ID != "" {
		if stickyNode, stickyConn, hasSticky := hub.stickyPairFor(fromNodeID, entryConn.ID, hopIndex); hasSticky && stickyNode != "" && stickyConn != "" && !visitedNodes[stickyNode] {
			for _, cand := range active[stickyNode] {
				if cand.ID == stickyConn && connectionDestMatches(entryConn, cand) {
					hub.rememberStickyPair(fromNodeID, entryConn.ID, hopIndex, stickyNode, stickyConn, time.Now())
					return stickyNode, cand, true
				}
			}
		}
	}

	// Legacy synthetic test structs without ID cannot participate in multi-entry
	// deduplication cleanly; evaluate all candidates directly by score.
	if entryConn.ID == "" {
		var bestNodeID string
		var bestConn storedConnection
		bestScore := -math.MaxFloat64
		bestTimeDelta := math.MaxFloat64
		found := false

		for candNodeID, candList := range active {
			if candNodeID == fromNodeID || visitedNodes[candNodeID] {
				continue
			}
			for _, cand := range candList {
				ev := relayEvidenceTier(fromNodeID, preferredTargetNodeID, candNodeID, cand)
				if ev == 0 || !connectionDestMatches(entryConn, cand) {
					continue
				}
				sc, dt := scoreChainCandidate(fromNodeID, candNodeID, entryConn, cand, ev, clockOffsets, false)
				if !found || sc > bestScore || (sc == bestScore && dt < bestTimeDelta) {
					found = true
					bestNodeID = candNodeID
					bestConn = cand
					bestScore = sc
					bestTimeDelta = dt
				}
			}
		}
		return bestNodeID, bestConn, found
	}

	// Gather all competing chained entries on fromNodeID so one connection does not
	// greedily steal a better-fitting downstream relay from a concurrent sibling.
	type indexedEntry struct {
		key             string
		conn            storedConnection
		preferredTarget string
	}
	entries := make([]indexedEntry, 0, len(active[fromNodeID])+1)
	seenTargetEntry := false
	for _, c := range active[fromNodeID] {
		if c.ID == "" {
			continue
		}
		epID := connectionChainEndpointID(c)
		if epID == "" && c.ID != entryConn.ID {
			continue
		}
		pref := ""
		if c.ID == entryConn.ID {
			seenTargetEntry = true
			pref = preferredTargetNodeID
		} else if epID != "" && preferredTargetByEndpoint != nil {
			pref = preferredTargetByEndpoint[epID]
		}
		entries = append(entries, indexedEntry{
			key:             c.ID,
			conn:            c,
			preferredTarget: pref,
		})
	}
	if !seenTargetEntry {
		entries = append(entries, indexedEntry{
			key:             entryConn.ID,
			conn:            entryConn,
			preferredTarget: preferredTargetNodeID,
		})
	}

	edges := make([]scoredChainEdge, 0, len(entries)*2)
	for _, ent := range entries {
		var stickyNode, stickyConn string
		hasSticky := false
		if hub != nil {
			stickyNode, stickyConn, hasSticky = hub.stickyPairFor(fromNodeID, ent.conn.ID, hopIndex)
		}
		for candNodeID, candList := range active {
			if candNodeID == fromNodeID || visitedNodes[candNodeID] {
				continue
			}
			for i, cand := range candList {
				ev := relayEvidenceTier(fromNodeID, ent.preferredTarget, candNodeID, cand)
				if ev == 0 || !connectionDestMatches(ent.conn, cand) {
					continue
				}
				candKey := candNodeID + "\x00" + cand.ID
				if cand.ID == "" {
					candKey = candNodeID + "\x00#" + strconv.Itoa(i)
				}
				isSticky := hasSticky && stickyNode == candNodeID && cand.ID != "" && stickyConn == cand.ID
				sc, dt := scoreChainCandidate(fromNodeID, candNodeID, ent.conn, cand, ev, clockOffsets, isSticky)
				edges = append(edges, scoredChainEdge{
					entryKey:       ent.key,
					candNodeID:     candNodeID,
					candKey:        candKey,
					candConn:       cand,
					score:          sc,
					absTimeDeltaMs: dt,
				})
			}
		}
	}

	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].score != edges[j].score {
			return edges[i].score > edges[j].score
		}
		if edges[i].absTimeDeltaMs != edges[j].absTimeDeltaMs {
			return edges[i].absTimeDeltaMs < edges[j].absTimeDeltaMs
		}
		if edges[i].entryKey != edges[j].entryKey {
			return edges[i].entryKey < edges[j].entryKey
		}
		return edges[i].candKey < edges[j].candKey
	})

	usedEntries := make(map[string]bool, len(entries))
	usedCands := make(map[string]bool, len(entries))
	now := time.Now()
	for _, edge := range edges {
		if usedEntries[edge.entryKey] || usedCands[edge.candKey] {
			continue
		}
		usedEntries[edge.entryKey] = true
		usedCands[edge.candKey] = true
		if hub != nil && edge.candConn.ID != "" {
			hub.rememberStickyPair(fromNodeID, edge.entryKey, hopIndex, edge.candNodeID, edge.candConn.ID, now)
		}
		if edge.entryKey == entryConn.ID {
			return edge.candNodeID, edge.candConn, true
		}
	}
	return "", storedConnection{}, false
}

// matchOriginEntryScored finds the best matching upstream connection on upstreamID
// for curConn using multi-dimensional scoring, sticky reverse hints, and 1-to-1 matching.
func matchOriginEntryScored(
	active map[string][]storedConnection,
	hub *connectionsHub,
	upstreamID string,
	curNodeID string,
	curConn storedConnection,
) (storedConnection, bool) {
	candidates := active[upstreamID]
	if len(candidates) == 0 {
		return storedConnection{}, false
	}
	var clockOffsets map[string]time.Duration
	stickyEntryID := ""
	if hub != nil {
		clockOffsets = hub.clockOffsetsSnapshot()
		if curNodeID != "" && curConn.ID != "" {
			if upID, ok := hub.stickyUpstreamFor(upstreamID, curNodeID, curConn.ID); ok {
				stickyEntryID = upID
			}
		}
	}

	// If curConn has an ID and curNodeID has multiple concurrent relays from upstreamID,
	// assign 1-to-1 globally so two relays don't map back to the same origin connection.
	if curNodeID != "" && curConn.ID != "" && len(active[curNodeID]) > 1 {
		var relays []storedConnection
		seenCur := false
		for _, r := range active[curNodeID] {
			if !r.IsChainRelay && r.ID != curConn.ID {
				continue
			}
			if r.RelayUpstreamNodeID != "" && r.RelayUpstreamNodeID != upstreamID && r.ID != curConn.ID {
				continue
			}
			if r.ID == curConn.ID {
				seenCur = true
			}
			if r.ID != "" {
				relays = append(relays, r)
			}
		}
		if seenCur && len(relays) > 1 {
			edges := make([]scoredChainEdge, 0, len(relays)*2)
			for _, rel := range relays {
				relStickyUp := ""
				if hub != nil {
					relStickyUp, _ = hub.stickyUpstreamFor(upstreamID, curNodeID, rel.ID)
				}
				for i, up := range candidates {
					if connectionChainEndpointID(up) == "" || !connectionDestMatches(up, rel) {
						continue
					}
					upKey := up.ID
					if upKey == "" {
						upKey = "#" + strconv.Itoa(i)
					}
					isSticky := relStickyUp != "" && up.ID == relStickyUp
					sc, dt := scoreChainCandidate(upstreamID, curNodeID, up, rel, 3, clockOffsets, isSticky)
					edges = append(edges, scoredChainEdge{
						entryKey:       upKey,
						candKey:        rel.ID,
						candConn:       up,
						score:          sc,
						absTimeDeltaMs: dt,
					})
				}
			}
			sort.SliceStable(edges, func(i, j int) bool {
				if edges[i].score != edges[j].score {
					return edges[i].score > edges[j].score
				}
				if edges[i].absTimeDeltaMs != edges[j].absTimeDeltaMs {
					return edges[i].absTimeDeltaMs < edges[j].absTimeDeltaMs
				}
				return edges[i].entryKey < edges[j].entryKey
			})
			usedUp := make(map[string]bool)
			usedRel := make(map[string]bool)
			for _, edge := range edges {
				if usedUp[edge.entryKey] || usedRel[edge.candKey] {
					continue
				}
				usedUp[edge.entryKey] = true
				usedRel[edge.candKey] = true
				if edge.candKey == curConn.ID {
					return edge.candConn, true
				}
			}
		}
	}

	var best storedConnection
	bestScore := -math.MaxFloat64
	bestTimeDelta := math.MaxFloat64
	found := false
	for _, cand := range candidates {
		if connectionChainEndpointID(cand) == "" || !connectionDestMatches(cand, curConn) {
			continue
		}
		isSticky := stickyEntryID != "" && cand.ID == stickyEntryID
		sc, dt := scoreChainCandidate(upstreamID, curNodeID, cand, curConn, 3, clockOffsets, isSticky)
		if !found || sc > bestScore || (sc == bestScore && dt < bestTimeDelta) {
			found = true
			best = cand
			bestScore = sc
			bestTimeDelta = dt
		}
	}
	return best, found
}
