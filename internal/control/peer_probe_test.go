package control

import "testing"

func TestGetNodePublicIPv6(t *testing.T) {
	nodeV4Only := Node{
		ID:          "node-v4",
		IPAddresses: []string{"198.51.100.1", "10.0.0.1"},
	}
	if got := getNodePublicIPv6(nodeV4Only); got != "" {
		t.Errorf("expected empty ipv6 for v4 only node, got %s", got)
	}

	nodeDualStack := Node{
		ID:          "node-dual",
		IPAddresses: []string{"198.51.100.2", "2606:4700:4700::1111", "fe80::1"},
	}
	if got := getNodePublicIPv6(nodeDualStack); got != "2606:4700:4700::1111" {
		t.Errorf("expected 2606:4700:4700::1111, got %s", got)
	}
}
