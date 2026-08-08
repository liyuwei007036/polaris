package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/liyuwei007036/polaris/internal/nginxroute"
)

// nginxSiteEdit is a site file rewritten to free a port, kept together with its
// original bytes so the change can be undone when Nginx rejects the result.
type nginxSiteEdit struct {
	file     string
	original []byte
	routes   []nginxroute.Route
	// defaultFor is the public port whose unmatched traffic belongs to this
	// site, set only for a catch-all site. Such a site has no name to route by,
	// but everything the router does not recognise is precisely what it used to
	// serve, so it becomes the group's default backend.
	defaultFor     uint16
	defaultBackend uint16
	summary        string
}

// nginxTakeoverRecord remembers a site that was moved aside. The move happens
// once, but the route back to it has to be re-added to every configuration the
// control plane sends afterwards: by then the site no longer holds the public
// port, so it can no longer be discovered from `nginx -T`, and rebuilding the
// file without its route would take the operator's own site offline.
type nginxTakeoverRecord struct {
	File        string   `json:"file"`
	Port        uint16   `json:"port"`
	BackendPort uint16   `json:"backend_port"`
	Names       []string `json:"names"`
	// CatchAll marks a site that had no routable name and became the group's
	// default backend instead.
	CatchAll bool `json:"catch_all,omitempty"`
}

func nginxTakeoverStatePath(dataDir string) string {
	return filepath.Join(dataDir, "desired-state", "nginx-takeover.json")
}

func loadNginxTakeover(dataDir string) []nginxTakeoverRecord {
	content, err := os.ReadFile(nginxTakeoverStatePath(dataDir))
	if err != nil {
		return nil
	}
	var records []nginxTakeoverRecord
	if json.Unmarshal(content, &records) != nil {
		return nil
	}
	return records
}

func saveNginxTakeover(dataDir string, records []nginxTakeoverRecord) error {
	content, err := json.Marshal(records)
	if err != nil {
		return err
	}
	directory := filepath.Dir(nginxTakeoverStatePath(dataDir))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(nginxTakeoverStatePath(dataDir), content, 0o600)
}

func takeoverRecordRoutes(records []nginxTakeoverRecord) []nginxroute.Route {
	var routes []nginxroute.Route
	for _, record := range records {
		// A catch-all site is reached through the group's default backend, not
		// through a named route.
		if record.CatchAll {
			continue
		}
		for _, name := range record.Names {
			routes = append(routes, nginxroute.Route{
				ListenAddress: "0.0.0.0", Port: record.Port, SNI: name,
				BackendAddress: "127.0.0.1", BackendPort: record.BackendPort,
			})
		}
	}
	return routes
}

func appendTakeoverRecords(records []nginxTakeoverRecord, edits []nginxSiteEdit) []nginxTakeoverRecord {
	for _, edit := range edits {
		if edit.defaultFor != 0 {
			records = append(records, nginxTakeoverRecord{
				File: edit.file, Port: edit.defaultFor,
				BackendPort: edit.defaultBackend, CatchAll: true,
			})
			continue
		}
		if len(edit.routes) == 0 {
			continue
		}
		names := make([]string, 0, len(edit.routes))
		for _, route := range edit.routes {
			names = append(names, route.SNI)
		}
		records = append(records, nginxTakeoverRecord{
			File: edit.file, Port: edit.routes[0].Port,
			BackendPort: edit.routes[0].BackendPort, Names: names,
		})
	}
	return records
}

// takeOverManagedPorts moves aside the http sites holding the ports the managed
// SNI router needs, so both can keep serving: the router owns the socket and
// forwards each site's own names back to it on loopback.
//
// This is what lets polaris coexist with an Nginx that was installed first and
// already serves a site on 443. Only a site whose names can be matched against
// a ClientHello is moved; a catch-all, a stream block or polaris's own file is
// left alone, and the port conflict surfaces from `nginx -t` as before.
func takeOverManagedPorts(ctx context.Context, configuration string, known []nginxTakeoverRecord) ([]nginxSiteEdit, error) {
	ports := managedListenPorts(configuration)
	if len(ports) == 0 {
		return nil, nil
	}
	// A port whose default backend already belongs to a site moved earlier
	// cannot take a second one.
	claimedDefaults := map[uint16]bool{}
	for _, record := range known {
		if record.CatchAll {
			claimedDefaults[record.Port] = true
		}
	}
	dump, err := exec.CommandContext(ctx, "nginx", "-T").CombinedOutput()
	if err != nil {
		// Without the loaded configuration nothing can be concluded about who
		// holds the port, so the deploy proceeds as it did before.
		return nil, nil
	}
	sockets, _ := listeningSockets(ctx)
	reserved := map[uint16]bool{}
	for _, port := range ports {
		reserved[port] = true
	}
	edits := []nginxSiteEdit{}
	for _, port := range ports {
		for _, server := range nginxHTTPServersOnPort(string(dump), port) {
			if strings.HasPrefix(server.file, filepath.Dir(managedNginxConfig)) {
				continue
			}
			if editedFile(edits, server.file) {
				continue
			}
			// A site with no routable name still has to be moved: leaving it on
			// the port is the conflict this whole path exists to prevent. It
			// becomes the group's default backend, which matches what a
			// catch-all server block already meant.
			names := routableNginxServerNames(server)
			catchAll := len(names) == 0
			if catchAll && (claimedDefaults[port] || defaultTaken(edits, port)) {
				return edits, errors.New("端口 " + strconv.Itoa(int(port)) + " 上有多个未指定域名的 Nginx 站点，无法自动区分，请为它们填写 server_name")
			}
			original, err := os.ReadFile(server.file)
			if err != nil {
				return edits, errors.New("读取 Nginx 站点配置失败：" + err.Error() + permissionHint(err))
			}
			backendPort, ok := freeLoopbackPort(sockets, reserved)
			if !ok {
				return edits, errors.New("没有可用的本机端口用于转移现有 Nginx 站点")
			}
			rewritten, changed := rewriteNginxListenPort(string(original), port, backendPort)
			if !changed {
				continue
			}
			if err := os.WriteFile(server.file, []byte(rewritten), 0o644); err != nil {
				return edits, errors.New("改写 Nginx 站点配置失败：" + err.Error() + permissionHint(err))
			}
			edit := nginxSiteEdit{
				file:     server.file,
				original: original,
				summary:  server.file + " 的 " + strconv.Itoa(int(port)) + " 端口已改为 127.0.0.1:" + strconv.Itoa(int(backendPort)),
			}
			if catchAll {
				edit.defaultFor, edit.defaultBackend = port, backendPort
			}
			for _, name := range names {
				edit.routes = append(edit.routes, nginxroute.Route{
					ListenAddress: "0.0.0.0", Port: port, SNI: name,
					BackendAddress: "127.0.0.1", BackendPort: backendPort,
				})
			}
			edits = append(edits, edit)
		}
	}
	if len(edits) == 0 {
		return nil, nil
	}
	return edits, nil
}

func editedFile(edits []nginxSiteEdit, file string) bool {
	for _, edit := range edits {
		if edit.file == file {
			return true
		}
	}
	return false
}

func defaultTaken(edits []nginxSiteEdit, port uint16) bool {
	for _, edit := range edits {
		if edit.defaultFor == port {
			return true
		}
	}
	return false
}

// applyTakeoverDefaults points each group's fallback at the catch-all site that
// used to own the port. Named routes are merged separately; this covers the
// traffic no name matches.
func applyTakeoverDefaults(configuration string, defaults []nginxTakeoverRecord) (string, error) {
	var err error
	for _, record := range defaults {
		if !record.CatchAll {
			continue
		}
		configuration, err = nginxroute.SetDefaultBackend(configuration, "0.0.0.0", record.Port, "127.0.0.1", record.BackendPort)
		if err != nil {
			return "", err
		}
	}
	return configuration, nil
}

func takeOverDefaults(edits []nginxSiteEdit) []nginxTakeoverRecord {
	var defaults []nginxTakeoverRecord
	for _, edit := range edits {
		if edit.defaultFor != 0 {
			defaults = append(defaults, nginxTakeoverRecord{
				Port: edit.defaultFor, BackendPort: edit.defaultBackend, CatchAll: true,
			})
		}
	}
	return defaults
}

// restoreNginxSites puts every rewritten site file back. It runs when the
// resulting configuration is rejected, so a failed deploy never leaves the
// operator's own sites bound to a port nothing forwards to.
func restoreNginxSites(edits []nginxSiteEdit) {
	for _, edit := range edits {
		_ = os.WriteFile(edit.file, edit.original, 0o644)
	}
}

// nginxApplySucceeded records the sites that were moved aside so later deploys
// keep routing to them, and reports the move to the operator.
func nginxApplySucceeded(dataDir string, edits []nginxSiteEdit) TaskResult {
	summary := "Nginx configuration validated and reloaded"
	if len(edits) == 0 {
		return TaskResult{Status: "succeeded", Summary: summary}
	}
	summary += "；" + takeOverSummary(edits)
	// Nginx already serves the new configuration at this point, so failing to
	// record the move must not undo the site files: the record can be rebuilt
	// by hand, a half-reverted host cannot.
	if err := saveNginxTakeover(dataDir, appendTakeoverRecords(loadNginxTakeover(dataDir), edits)); err != nil {
		summary += "；记录接管状态失败：" + err.Error()
	}
	return TaskResult{Status: "succeeded", Summary: summary}
}

func takeOverRoutes(edits []nginxSiteEdit) []nginxroute.Route {
	var routes []nginxroute.Route
	for _, edit := range edits {
		routes = append(routes, edit.routes...)
	}
	return routes
}

func takeOverSummary(edits []nginxSiteEdit) string {
	summaries := make([]string, 0, len(edits))
	for _, edit := range edits {
		summaries = append(summaries, edit.summary)
	}
	return strings.Join(summaries, "；")
}

// managedListenPorts reads the ports the compiled router configuration binds.
func managedListenPorts(configuration string) []uint16 {
	var ports []uint16
	seen := map[uint16]bool{}
	for _, raw := range strings.Split(configuration, "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if !strings.HasPrefix(line, "listen ") {
			continue
		}
		port, ok := nginxListenPort(strings.TrimSuffix(strings.TrimPrefix(line, "listen "), ";"))
		if !ok || seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	return ports
}

// freeLoopbackPort picks a port no process listens on. It starts above the
// range the control plane hands to sing-box backends so the two allocators
// cannot collide.
func freeLoopbackPort(sockets []listeningSocket, reserved map[uint16]bool) (uint16, bool) {
	taken := map[uint16]bool{}
	for _, socket := range sockets {
		taken[socket.port] = true
	}
	for port := uint16(40000); port < 50000; port++ {
		if taken[port] || reserved[port] || !loopbackPortFree(port) {
			continue
		}
		reserved[port] = true
		return port, true
	}
	return 0, false
}

// loopbackPortFree binds the port to be sure it is free. The socket listing can
// be unavailable or incomplete — `ss` may be missing entirely — and moving a
// site onto an occupied port would take that site down instead of saving it.
func loopbackPortFree(port uint16) bool {
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(int(port)))
	if err != nil {
		return false
	}
	return listener.Close() == nil
}

// foreignPortOwners names the processes other than Nginx still holding a port
// the router needs. Nginx is expected to hold them — it either already serves
// the router configuration or just had a site moved aside — but nothing else
// can be moved out of the way, so the deploy has to report it rather than let
// Nginx fail to bind and roll everything back.
func foreignPortOwners(ctx context.Context, configuration string) []string {
	ports := managedListenPorts(configuration)
	if len(ports) == 0 {
		return nil
	}
	sockets, ok := listeningSockets(ctx)
	if !ok {
		return nil
	}
	var owners []string
	reported := map[string]bool{}
	for _, port := range ports {
		for _, socket := range sockets {
			if socket.network != "tcp" || socket.port != port || socket.process == "nginx" {
				continue
			}
			owner := "TCP/" + strconv.Itoa(int(port)) + " 已被 " + socket.process + " 占用"
			if reported[owner] {
				continue
			}
			reported[owner] = true
			owners = append(owners, owner)
		}
	}
	return owners
}

// nginxHTTPServer is one server block in the http context, together with the
// file that declares it. `nginx -T` prints every file it loaded behind a
// "# configuration file <path>:" marker, which is what makes the block
// traceable back to something that can be edited.
type nginxHTTPServer struct {
	file  string
	names []string
	// listens are the raw arguments of each listen directive in the block,
	// semicolon stripped, in the order they appear.
	listens []string
}

// nginxHTTPServersOnPort finds the http server blocks that bind a port, given
// the output of `nginx -T`.
//
// Blocks inside a stream context are skipped: only the http side can be moved
// aside to free the port for the managed SNI router, and a stream block on the
// same port is a conflict the operator has to resolve.
func nginxHTTPServersOnPort(dump string, port uint16) []nginxHTTPServer {
	var servers []nginxHTTPServer
	file := ""
	depth := 0
	// contexts[d] names the block that opened at depth d, so a server block can
	// tell whether it sits under http or under stream.
	contexts := map[int]string{}
	var current *nginxHTTPServer
	currentDepth := 0
	for _, raw := range strings.Split(dump, "\n") {
		if marker := strings.TrimPrefix(raw, "# configuration file "); marker != raw {
			file = strings.TrimSuffix(strings.TrimSpace(marker), ":")
			continue
		}
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "}") {
			if current != nil && depth == currentDepth {
				if serverBindsPort(*current, port) {
					servers = append(servers, *current)
				}
				current = nil
			}
			delete(contexts, depth)
			depth--
			continue
		}
		if strings.HasSuffix(line, "{") {
			name := strings.Fields(strings.TrimSuffix(line, "{"))
			depth++
			if len(name) > 0 {
				contexts[depth] = name[0]
			}
			// `nginx -T` prints an included file verbatim, so a site under
			// sites-enabled starts at the top level with no enclosing http
			// block to check against. Only a stream context is conclusive, and
			// a stream server has no server_name — which is what the caller
			// requires before it will move a block anywhere.
			if len(name) > 0 && name[0] == "server" && contexts[depth-1] != "stream" {
				current = &nginxHTTPServer{file: file}
				currentDepth = depth
			}
			continue
		}
		if current == nil {
			continue
		}
		directive := strings.TrimSuffix(line, ";")
		fields := strings.Fields(directive)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "listen":
			current.listens = append(current.listens, strings.Join(fields[1:], " "))
		case "server_name":
			current.names = append(current.names, fields[1:]...)
		}
	}
	return servers
}

func serverBindsPort(server nginxHTTPServer, port uint16) bool {
	for _, listen := range server.listens {
		if listenPort, ok := nginxListenPort(listen); ok && listenPort == port {
			return true
		}
	}
	return false
}

// nginxListenPort reads the port out of a listen directive's arguments, which
// take the forms "443 ssl", "0.0.0.0:443", "[::]:443 ssl" and "*:443".
func nginxListenPort(arguments string) (uint16, bool) {
	fields := strings.Fields(arguments)
	if len(fields) == 0 {
		return 0, false
	}
	address := fields[0]
	if index := strings.LastIndex(address, ":"); index >= 0 {
		address = address[index+1:]
	}
	port, err := strconv.ParseUint(address, 10, 16)
	if err != nil {
		return 0, false
	}
	return uint16(port), true
}

// routableNginxServerNames keeps the names that can be matched against a TLS
// ClientHello. Catch-all names ("_", "*", empty) and wildcards cannot be used
// as SNI routes, and a server that has nothing else is not worth relocating:
// there would be no way to send traffic back to it.
func routableNginxServerNames(server nginxHTTPServer) []string {
	var names []string
	seen := map[string]bool{}
	for _, name := range server.names {
		name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
		if name == "" || name == "_" || strings.ContainsAny(name, "*~") || seen[name] {
			continue
		}
		if !nginxroute.ValidSNI(name) {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// rewriteNginxListenPort rewrites every listen directive that binds `from` so
// it binds `to` on loopback instead, leaving the rest of the directive (ssl,
// http2, default_server, …) untouched.
func rewriteNginxListenPort(configuration string, from, to uint16) (string, bool) {
	lines := strings.Split(configuration, "\n")
	changed := false
	for index, raw := range lines {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if !strings.HasPrefix(line, "listen ") {
			continue
		}
		arguments := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, "listen")), ";")
		listenPort, ok := nginxListenPort(arguments)
		if !ok || listenPort != from {
			continue
		}
		fields := strings.Fields(arguments)
		// default_server on a relocated block would make it the fallback for a
		// port it no longer owns, and IPv6 loopback is covered by 127.0.0.1.
		options := []string{}
		for _, option := range fields[1:] {
			if option == "default_server" {
				continue
			}
			options = append(options, option)
		}
		indent := raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))]
		rewritten := indent + "listen 127.0.0.1:" + strconv.Itoa(int(to))
		if len(options) > 0 {
			rewritten += " " + strings.Join(options, " ")
		}
		lines[index] = rewritten + ";"
		changed = true
	}
	return strings.Join(lines, "\n"), changed
}
