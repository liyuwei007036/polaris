package nginxroute

import (
	"strings"
	"testing"
)

func TestCompileAndMergePassthroughRoutes(t *testing.T) {
	managed := []Route{
		{ListenAddress: "0.0.0.0", Port: 443, SNI: "www.icloud.com", BackendAddress: "127.0.0.1", BackendPort: 20001},
		{ListenAddress: "0.0.0.0", Port: 443, SNI: "sg-master.example.com", BackendAddress: "127.0.0.1", BackendPort: 20000},
	}
	configuration, err := Compile(managed)
	if err != nil {
		t.Fatal(err)
	}
	marker := Marker(GroupName("0.0.0.0", 443))
	if strings.Count(configuration, marker) != 1 {
		t.Fatalf("compiled configuration marker count = %d\n%s", strings.Count(configuration, marker), configuration)
	}
	merged, err := MergePassthrough(configuration, []Route{
		{ListenAddress: "0.0.0.0", Port: 443, SNI: "s2a.example.com", BackendAddress: "127.0.0.1", BackendPort: 10444},
		{ListenAddress: "0.0.0.0", Port: 443, SNI: "pay.example.com", BackendAddress: "127.0.0.1", BackendPort: 10445},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`s2a.example.com "127.0.0.1:10444";`,
		`pay.example.com "127.0.0.1:10445";`,
		`sg-master.example.com "127.0.0.1:20000";`,
		`www.icloud.com "127.0.0.1:20001";`,
	} {
		if !strings.Contains(merged, expected) {
			t.Fatalf("merged configuration missing %q\n%s", expected, merged)
		}
	}
	if strings.Count(merged, "listen 0.0.0.0:443;") != 1 {
		t.Fatalf("merged configuration owns TCP/443 %d times", strings.Count(merged, "listen 0.0.0.0:443;"))
	}
	if strings.Count(merged, "listen [::]:443;") != 1 {
		t.Fatalf("merged configuration missing dual-stack IPv6 listen for TCP/443: %s", merged)
	}
	if !strings.Contains(merged, "proxy_connect_timeout 10s;") || !strings.Contains(merged, "proxy_timeout 10m;") {
		t.Fatalf("merged configuration omitted proxy timeouts:\n%s", merged)
	}
	if !strings.Contains(merged, "proxy_protocol on;") {
		t.Fatalf("merged configuration omitted proxy_protocol:\n%s", merged)
	}
}

func TestMergePassthroughRejectsMissingGroupAndManagedSNIConflict(t *testing.T) {
	configuration, err := Compile([]Route{{
		ListenAddress: "0.0.0.0", Port: 443, SNI: "managed.example.com", BackendAddress: "127.0.0.1", BackendPort: 20000,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MergePassthrough(configuration, []Route{{
		ListenAddress: "0.0.0.0", Port: 8443, SNI: "external.example.com", BackendAddress: "127.0.0.1", BackendPort: 10444,
	}}); err == nil {
		t.Fatal("accepted passthrough route for a group the master does not manage")
	}
	if _, err := MergePassthrough(configuration, []Route{{
		ListenAddress: "0.0.0.0", Port: 443, SNI: "managed.example.com", BackendAddress: "127.0.0.1", BackendPort: 10444,
	}}); err == nil {
		t.Fatal("accepted passthrough route that conflicts with a managed SNI")
	}
}

func TestCompileAutoFallbackRoute(t *testing.T) {
	routes := []Route{
		{ListenAddress: "0.0.0.0", Port: 443, SNI: "xhttp.example.com", BackendAddress: "127.0.0.1", BackendPort: 20000},
		{ListenAddress: "0.0.0.0", Port: 443, SNI: "www.microsoft.com", BackendAddress: "127.0.0.1", BackendPort: 20001, IsDefault: true},
	}
	configuration, err := Compile(routes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(configuration, `default "127.0.0.1:20001"; # auto-fallback`) {
		t.Fatalf("expected auto-fallback to Reality backend 127.0.0.1:20001, got:\n%s", configuration)
	}

	// Verify SetDefaultBackend can override it (e.g. host operator had an existing website)
	overridden, err := SetDefaultBackend(configuration, "0.0.0.0", 443, "127.0.0.1", 40000)
	if err != nil {
		t.Fatalf("SetDefaultBackend failed to override auto-fallback: %v", err)
	}
	if !strings.Contains(overridden, `default "127.0.0.1:40000";`) {
		t.Fatalf("expected overridden default, got:\n%s", overridden)
	}
	if strings.Contains(overridden, "# auto-fallback") {
		t.Fatalf("overridden configuration still contains auto-fallback comment:\n%s", overridden)
	}

	// Verify second claim is rejected
	if _, err := SetDefaultBackend(overridden, "0.0.0.0", 443, "127.0.0.1", 40001); err == nil {
		t.Fatal("expected second claim to be rejected")
	}
}
