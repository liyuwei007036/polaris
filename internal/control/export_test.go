package control

import (
	"context"
	"encoding/json"
)

// ExecForTest runs one statement against the store's database so a test can
// set up state the API deliberately does not expose, such as records old
// enough to fall outside the retention window. Compiled only for tests.
func (s *Store) ExecForTest(ctx context.Context, query string, args ...any) error {
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

// CountForTest returns the single integer a counting query produces.
// Compiled only for tests.
func (s *Store) CountForTest(ctx context.Context, query string, args ...any) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

// SetCloudflareAPIBaseForTest points the Cloudflare client at a test server.
// Compiled only for tests.
func SetCloudflareAPIBaseForTest(url string) { cloudflareAPI = url }

// PushConnectionsForTest injects a real-time connections snapshot exactly as
// an agent's push would. Connections are in-memory state, so this is the only
// way a test can populate them. Compiled only for tests.
func (s *Server) PushConnectionsForTest(nodeID, collectedAt string, connections json.RawMessage) {
	s.connHub.update(nodeConnectionsSnapshot{NodeID: nodeID, CollectedAt: collectedAt, Connections: connections})
}
