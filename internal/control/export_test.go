package control

import "encoding/json"

// SetCloudflareAPIBaseForTest points the Cloudflare client at a test server.
// Compiled only for tests.
func SetCloudflareAPIBaseForTest(url string) { cloudflareAPI = url }

// PushConnectionsForTest injects a real-time connections snapshot exactly as
// an agent's push would. Connections are in-memory state, so this is the only
// way a test can populate them. Compiled only for tests.
func (s *Server) PushConnectionsForTest(nodeID, collectedAt string, connections json.RawMessage) {
	s.connHub.update(nodeConnectionsSnapshot{NodeID: nodeID, CollectedAt: collectedAt, Connections: connections})
}
