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

func TestIPLocatorLocatesFirewallCIDRs(t *testing.T) {
	locator, err := newIPLocator()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		cidr string
		want string
	}{
		{cidr: "8.8.8.0/24", want: "United States"},
		{cidr: "240e:3b7:3272:d8d0:db09:c067:8d59:539e/128", want: "广东省"},
		{cidr: "192.168.1.0/24", want: "内网"},
		{cidr: "0.0.0.0/0", want: "所有 IPv4 地址"},
		{cidr: "::/0", want: "所有 IPv6 地址"},
		{cidr: "", want: "所有来源"},
	}
	for _, test := range tests {
		if location := locator.LocateCIDR(test.cidr); !strings.Contains(location, test.want) {
			t.Errorf("LocateCIDR(%q) = %q, want location containing %q", test.cidr, location, test.want)
		}
	}
}
