package control

import (
	"strings"
	"testing"
)

func TestIPLocatorUsesEmbeddedIPv4AndIPv6Databases(t *testing.T) {
	locator, err := newIPLocator()
	if err != nil {
		t.Fatal(err)
	}
	if location := locator.Locate("8.8.8.8"); !strings.Contains(location, "United States") {
		t.Fatalf("unexpected IPv4 location %q", location)
	}
	if location := locator.Locate("240e:3b7:3272:d8d0:db09:c067:8d59:539e"); !strings.Contains(location, "广东省") {
		t.Fatalf("unexpected IPv6 location %q", location)
	}
	if location := locator.Locate("192.168.1.10"); location != "内网" {
		t.Fatalf("private IP location = %q", location)
	}
	if location := locator.Locate("127.0.0.1"); location != "本机" {
		t.Fatalf("loopback IP location = %q", location)
	}
}
