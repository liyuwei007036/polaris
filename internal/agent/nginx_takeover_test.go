package agent

import (
	"net"
	"strings"
	"testing"

	"github.com/liyuwei007036/polaris/internal/nginxroute"
	"github.com/liyuwei007036/polaris/internal/wire"
)

const sampleNginxDump = `# configuration file /etc/nginx/nginx.conf:
user www-data;
events {
    worker_connections 4096;
}
http {
    include /etc/nginx/sites-enabled/*;
}
stream {
    server {
        listen 8853;
        proxy_pass 127.0.0.1:5353;
    }
}

# configuration file /etc/nginx/sites-enabled/blog:
server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2 default_server;
    server_name www.example.com blog.example.com;
    ssl_certificate /etc/ssl/blog.pem;
    location / {
        root /var/www/blog;
    }
}
server {
    listen 80 default_server;
    server_name _;
    return 301 https://$host$request_uri;
}
`

func TestNginxHTTPServersOnPortFindsTheBlockHoldingThePort(t *testing.T) {
	servers := nginxHTTPServersOnPort(sampleNginxDump, 443)
	if len(servers) != 1 {
		t.Fatalf("servers on 443 = %#v", servers)
	}
	server := servers[0]
	if server.file != "/etc/nginx/sites-enabled/blog" {
		t.Fatalf("server file = %q", server.file)
	}
	if len(server.listens) != 2 || server.listens[0] != "443 ssl http2" {
		t.Fatalf("listens = %#v", server.listens)
	}
	if strings.Join(server.names, ",") != "www.example.com,blog.example.com" {
		t.Fatalf("names = %#v", server.names)
	}
}

// The stream block on 8853 is a conflict the operator has to resolve, not
// something that can be moved aside, so it must never be reported.
func TestNginxHTTPServersOnPortSkipsStreamBlocks(t *testing.T) {
	if servers := nginxHTTPServersOnPort(sampleNginxDump, 8853); len(servers) != 0 {
		t.Fatalf("stream server was reported as http: %#v", servers)
	}
}

func TestNginxHTTPServersOnPortFindsThePlainHTTPBlock(t *testing.T) {
	servers := nginxHTTPServersOnPort(sampleNginxDump, 80)
	if len(servers) != 1 || len(servers[0].names) != 1 || servers[0].names[0] != "_" {
		t.Fatalf("servers on 80 = %#v", servers)
	}
}

func TestRoutableNginxServerNamesDropsWhatCannotBeAnSNI(t *testing.T) {
	names := routableNginxServerNames(nginxHTTPServer{
		names: []string{"www.example.com", "_", "*.example.com", "~^web\\.", "WWW.EXAMPLE.COM", "blog.example.com."},
	})
	if strings.Join(names, ",") != "www.example.com,blog.example.com" {
		t.Fatalf("routable names = %#v", names)
	}
}

func TestRewriteNginxListenPortMovesOnlyTheMatchingDirectives(t *testing.T) {
	original := `server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2 default_server;
    server_name www.example.com;
}
server {
    listen 80 default_server;
}
`
	rewritten, changed := rewriteNginxListenPort(original, 443, 8443)
	if !changed {
		t.Fatal("no listen directive was rewritten")
	}
	for _, expected := range []string{"    listen 127.0.0.1:8443 ssl http2;", "    listen 80 default_server;"} {
		if !strings.Contains(rewritten, expected) {
			t.Fatalf("rewritten configuration is missing %q:\n%s", expected, rewritten)
		}
	}
	// default_server has to go: the block no longer owns the port it was the
	// fallback for.
	if strings.Contains(rewritten, "127.0.0.1:8443 ssl http2 default_server") {
		t.Fatalf("default_server survived the move:\n%s", rewritten)
	}
	if strings.Count(rewritten, "127.0.0.1:8443") != 2 {
		t.Fatalf("both 443 listeners should have moved:\n%s", rewritten)
	}
}

func TestRewriteNginxListenPortLeavesUnrelatedConfigurationAlone(t *testing.T) {
	original := "server {\n    listen 8080;\n}\n"
	rewritten, changed := rewriteNginxListenPort(original, 443, 8443)
	if changed || rewritten != original {
		t.Fatalf("unrelated configuration was rewritten: %q", rewritten)
	}
}

const foreignStreamDump = `# configuration file /etc/nginx/nginx.conf:
user www-data;
http {
    server {
        listen 443 ssl;
        server_name www.example.com;
    }
}
stream {
    server {
        listen 443;
        proxy_pass 127.0.0.1:8443;
    }
    server {
        listen [::]:8853;
        proxy_pass 127.0.0.1:5353;
    }
    server {
        listen 8854 udp;
        proxy_pass 127.0.0.1:5354;
    }
    server {
        listen unix:/run/nginx.sock;
        proxy_pass 127.0.0.1:5355;
    }
}

# configuration file /etc/nginx/stream-conf.d/operator.conf:
server {
    listen 0.0.0.0:9443;
    proxy_pass 127.0.0.1:9444;
}

# configuration file /etc/nginx/stream-conf.d/polaris.conf:
server {
    listen 0.0.0.0:443;
    ssl_preread on;
    proxy_pass $polaris_0_0_0_0_443;
}

# configuration file /etc/nginx/sites-enabled/blog:
server {
    listen 8080;
    server_name blog.example.com;
}
`

// The http server on 443 and polaris's own file must never be reported; the
// operator's stream blocks — inline in nginx.conf or dropped into polaris's
// include directory — must be, minus UDP and unix-socket listens.
func TestNginxForeignStreamListensReportsOnlyUnmanagedTCPStreamListens(t *testing.T) {
	original := managedNginxConfig
	managedNginxConfig = "/etc/nginx/stream-conf.d/polaris.conf"
	defer func() { managedNginxConfig = original }()
	listens := nginxForeignStreamListens(foreignStreamDump)
	expected := []wire.StreamListen{
		{Address: "0.0.0.0", Port: 443, File: "/etc/nginx/nginx.conf"},
		{Address: "::", Port: 8853, File: "/etc/nginx/nginx.conf"},
		{Address: "0.0.0.0", Port: 9443, File: "/etc/nginx/stream-conf.d/operator.conf"},
	}
	if len(listens) != len(expected) {
		t.Fatalf("foreign stream listens = %#v", listens)
	}
	for index, listen := range expected {
		if listens[index] != listen {
			t.Fatalf("foreign stream listens[%d] = %#v, want %#v", index, listens[index], listen)
		}
	}
}

func TestStreamConflictSummariesMatchesOnlyTheExactSocket(t *testing.T) {
	configuration := "map $ssl_preread_server_name $polaris_0_0_0_0_443 {\n    default \"127.0.0.1:1\";\n}\nserver {\n    listen 0.0.0.0:443;\n    ssl_preread on;\n}\n"
	foreign := []wire.StreamListen{
		{Address: "0.0.0.0", Port: 443, File: "/etc/nginx/nginx.conf"},
		{Address: "127.0.0.1", Port: 443, File: "/etc/nginx/loopback.conf"},
		{Address: "0.0.0.0", Port: 8443, File: "/etc/nginx/other.conf"},
	}
	conflicts := streamConflictSummaries(configuration, foreign)
	if len(conflicts) != 1 || conflicts[0] != "TCP/443 已被 Nginx 配置文件 /etc/nginx/nginx.conf 中的 stream 服务占用" {
		t.Fatalf("stream conflicts = %#v", conflicts)
	}
}

// A bare-port listen in the operator's stream block binds the IPv4 wildcard,
// exactly the socket the compiled "listen 0.0.0.0:443" would bind.
func TestStreamConflictSummariesNormalizesWildcardSpellings(t *testing.T) {
	original := managedNginxConfig
	managedNginxConfig = "/etc/nginx/stream-conf.d/polaris.conf"
	defer func() { managedNginxConfig = original }()
	dump := "# configuration file /etc/nginx/nginx.conf:\nstream {\n    server {\n        listen 443;\n        proxy_pass 127.0.0.1:8443;\n    }\n}\n"
	configuration := "server {\n    listen 0.0.0.0:443;\n}\n"
	conflicts := streamConflictSummaries(configuration, nginxForeignStreamListens(dump))
	if len(conflicts) != 1 {
		t.Fatalf("stream conflicts = %#v", conflicts)
	}
}

func TestManagedListenPortsReadsTheRouterConfiguration(t *testing.T) {
	configuration, err := nginxroute.Compile([]nginxroute.Route{
		{ListenAddress: "0.0.0.0", Port: 443, SNI: "proxy.example.com", BackendAddress: "127.0.0.1", BackendPort: 20000},
		{ListenAddress: "0.0.0.0", Port: 8443, SNI: "other.example.com", BackendAddress: "127.0.0.1", BackendPort: 20001},
	})
	if err != nil {
		t.Fatal(err)
	}
	ports := managedListenPorts(configuration)
	if len(ports) != 2 || ports[0] != 443 || ports[1] != 8443 {
		t.Fatalf("managed listen ports = %#v", ports)
	}
}

// A site is moved aside once, but every later deploy rebuilds the router
// configuration from scratch. By then the site no longer holds the public port
// and cannot be rediscovered, so its route has to come from the record — or
// rebuilding the file silently takes the operator's own site offline.
func TestTakeoverRecordRoutesSurviveLaterDeploys(t *testing.T) {
	dataDir := t.TempDir()
	edits := []nginxSiteEdit{{
		file: "/etc/nginx/sites-enabled/blog",
		routes: []nginxroute.Route{
			{ListenAddress: "0.0.0.0", Port: 443, SNI: "www.example.com", BackendAddress: "127.0.0.1", BackendPort: 40000},
			{ListenAddress: "0.0.0.0", Port: 443, SNI: "blog.example.com", BackendAddress: "127.0.0.1", BackendPort: 40000},
		},
	}}
	if err := saveNginxTakeover(dataDir, appendTakeoverRecords(loadNginxTakeover(dataDir), edits)); err != nil {
		t.Fatal(err)
	}
	restored := takeoverRecordRoutes(loadNginxTakeover(dataDir))
	if len(restored) != 2 {
		t.Fatalf("restored routes = %#v", restored)
	}
	// The next deploy compiles a fresh configuration that knows nothing about
	// the moved site; merging the recorded routes has to put it back.
	fresh, err := nginxroute.Compile([]nginxroute.Route{
		{ListenAddress: "0.0.0.0", Port: 443, SNI: "proxy.example.com", BackendAddress: "127.0.0.1", BackendPort: 20000},
	})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := nginxroute.MergePassthrough(fresh, restored)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"proxy.example.com", "www.example.com", "blog.example.com", "127.0.0.1:40000"} {
		if !strings.Contains(merged, expected) {
			t.Fatalf("merged configuration lost %q:\n%s", expected, merged)
		}
	}
}

func TestLoadNginxTakeoverToleratesAMissingRecord(t *testing.T) {
	if records := loadNginxTakeover(t.TempDir()); records != nil {
		t.Fatalf("records from an empty data directory = %#v", records)
	}
}

// The stock Debian/Ubuntu site is `server_name _`, so a catch-all is the most
// likely thing found holding 443. Skipping it would leave the port occupied,
// which is the exact conflict this path exists to prevent.
func TestCatchAllSiteBecomesTheGroupDefaultBackend(t *testing.T) {
	configuration, err := nginxroute.Compile([]nginxroute.Route{
		{ListenAddress: "0.0.0.0", Port: 443, SNI: "proxy.example.com", BackendAddress: "127.0.0.1", BackendPort: 20000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(configuration, `default "`+nginxroute.DefaultBackendPlaceholder+`";`) {
		t.Fatalf("compiled configuration has no placeholder default:\n%s", configuration)
	}
	withDefault, err := applyTakeoverDefaults(configuration, []nginxTakeoverRecord{
		{File: "/etc/nginx/sites-enabled/default", Port: 443, BackendPort: 40000, CatchAll: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withDefault, `default "127.0.0.1:40000";`) {
		t.Fatalf("catch-all site did not become the default backend:\n%s", withDefault)
	}
	if strings.Contains(withDefault, nginxroute.DefaultBackendPlaceholder) {
		t.Fatalf("black-hole default survived:\n%s", withDefault)
	}
	// The named route must still be intact alongside it.
	if !strings.Contains(withDefault, "proxy.example.com") {
		t.Fatalf("named route was lost:\n%s", withDefault)
	}
}

// A catch-all is reached through the default backend, never through a named
// route, or the merge would emit a route with an empty SNI.
func TestCatchAllRecordProducesNoNamedRoute(t *testing.T) {
	records := []nginxTakeoverRecord{
		{File: "/etc/nginx/sites-enabled/default", Port: 443, BackendPort: 40000, CatchAll: true},
		{File: "/etc/nginx/sites-enabled/blog", Port: 443, BackendPort: 40001, Names: []string{"blog.example.com"}},
	}
	routes := takeoverRecordRoutes(records)
	if len(routes) != 1 || routes[0].SNI != "blog.example.com" {
		t.Fatalf("routes from mixed records = %#v", routes)
	}
	defaults := 0
	for _, record := range records {
		if record.CatchAll {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("catch-all records = %d", defaults)
	}
}

func TestSetDefaultBackendRejectsASecondClaim(t *testing.T) {
	configuration, err := nginxroute.Compile([]nginxroute.Route{
		{ListenAddress: "0.0.0.0", Port: 443, SNI: "proxy.example.com", BackendAddress: "127.0.0.1", BackendPort: 20000},
	})
	if err != nil {
		t.Fatal(err)
	}
	once, err := nginxroute.SetDefaultBackend(configuration, "0.0.0.0", 443, "127.0.0.1", 40000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nginxroute.SetDefaultBackend(once, "0.0.0.0", 443, "127.0.0.1", 40001); err == nil {
		t.Fatal("a second catch-all silently replaced the first")
	}
}

// `ss` can be missing entirely, and moving a site onto an occupied port would
// take that site down instead of saving it.
func TestFreeLoopbackPortSkipsAPortThatCannotBeBound(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:40000")
	if err != nil {
		t.Skipf("cannot occupy the first candidate port: %v", err)
	}
	defer occupied.Close()
	// No socket listing at all, so only the bind check can catch this.
	port, ok := freeLoopbackPort(nil, map[uint16]bool{})
	if !ok {
		t.Fatal("no free loopback port was found")
	}
	if port == 40000 {
		t.Fatal("an occupied port was handed out for a site relocation")
	}
}
