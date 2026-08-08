package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path"
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
// already serves a site — or an SNI router of the operator's own — on 443. A
// site whose names can be matched against a ClientHello keeps them as routes;
// anything else (a catch-all site, a stream server) becomes the group's
// default backend. Only polaris's own file is left alone.
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
	listening, _ := listeningSockets(ctx)
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
			backendPort, ok := freeLoopbackPort(listening, reserved)
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
	edits, err = takeOverStreamServers(configuration, string(dump), listening, reserved, claimedDefaults, edits)
	if err != nil {
		return edits, err
	}
	if len(edits) == 0 {
		return nil, nil
	}
	return edits, nil
}

// takeOverStreamServers moves aside the stream servers holding a socket the
// router needs. Such a server has no server_name, so it cannot keep named
// routes the way an http site does: it becomes the group's default backend,
// which is what it already meant. It used to receive every connection on this
// socket, and now receives every one the router does not recognise — an
// operator's own SNI router on 443 keeps working with nothing to migrate.
func takeOverStreamServers(configuration, dump string, listening []listeningSocket, reserved, claimedDefaults map[uint16]bool, edits []nginxSiteEdit) ([]nginxSiteEdit, error) {
	servers := nginxForeignStreamServers(dump)
	for _, socket := range managedListenSockets(configuration) {
		for _, server := range servers {
			if !streamServerBindsSocket(server, socket) || editedFile(edits, server.file) {
				continue
			}
			if claimedDefaults[socket.port] || defaultTaken(edits, socket.port) {
				return edits, errors.New("端口 " + strconv.Itoa(int(socket.port)) + " 上已有一个被接管的默认站点，无法同时接管 " + server.file + " 中的 stream 服务，请手动调整该配置")
			}
			original, err := os.ReadFile(server.file)
			if err != nil {
				return edits, errors.New("读取 Nginx stream 配置失败：" + err.Error() + permissionHint(err))
			}
			backendPort, ok := freeLoopbackPort(listening, reserved)
			if !ok {
				return edits, errors.New("没有可用的本机端口用于转移现有 Nginx stream 服务")
			}
			rewritten, changed := rewriteNginxListenPort(string(original), socket.port, backendPort)
			if !changed {
				continue
			}
			if err := os.WriteFile(server.file, []byte(rewritten), 0o644); err != nil {
				return edits, errors.New("改写 Nginx stream 配置失败：" + err.Error() + permissionHint(err))
			}
			edits = append(edits, nginxSiteEdit{
				file: server.file, original: original,
				defaultFor: socket.port, defaultBackend: backendPort,
				summary: server.file + " 的 stream 服务已从 " + strconv.Itoa(int(socket.port)) + " 端口改为 127.0.0.1:" + strconv.Itoa(int(backendPort)) + "，未匹配的连接仍转发给它",
			})
		}
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
	for _, socket := range managedListenSockets(configuration) {
		if seen[socket.port] {
			continue
		}
		seen[socket.port] = true
		ports = append(ports, socket.port)
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

// nginxStreamServer is one server block in a stream context, together with the
// file that declares it. It has no server_name — nothing about it can be
// matched against a ClientHello — which is what decides how it is taken over.
type nginxStreamServer struct {
	file string
	// listens are the raw arguments of each listen directive in the block,
	// semicolon stripped, in the order they appear.
	listens []string
}

// nginxForeignStreamServers finds the stream server blocks polaris does not
// manage, given the output of `nginx -T`.
//
// `nginx -T` prints an included file verbatim, so a server block is recognized
// as stream in two ways: it sits inside a literal stream context, or its file
// lives in polaris's own stream include directory — where an operator may have
// dropped a router of their own next to polaris's.
func nginxForeignStreamServers(dump string) []nginxStreamServer {
	// Nginx reports its files with forward slashes on every platform.
	managedFile := filepath.ToSlash(managedNginxConfig)
	streamDirectory := path.Dir(managedFile) + "/"
	var servers []nginxStreamServer
	file := ""
	depth := 0
	contexts := map[int]string{}
	var current *nginxStreamServer
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
				servers = append(servers, *current)
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
			if len(name) > 0 && name[0] == "server" && current == nil && file != managedFile &&
				(contexts[depth-1] == "stream" || (depth == 1 && strings.HasPrefix(file, streamDirectory))) {
				current = &nginxStreamServer{file: file}
				currentDepth = depth
			}
			continue
		}
		if current == nil {
			continue
		}
		fields := strings.Fields(strings.TrimSuffix(line, ";"))
		if len(fields) < 2 || fields[0] != "listen" {
			continue
		}
		current.listens = append(current.listens, strings.Join(fields[1:], " "))
	}
	return servers
}

// listenSocket is one address and port a listen directive binds, with the
// address normalized so the spellings nginx treats as the same socket compare
// equal. Two stream servers may share a port on different addresses, so only
// the exact same socket is the duplicate nginx refuses.
type listenSocket struct {
	address string
	port    uint16
}

// nginxListenSocket reads the socket out of a listen directive's arguments,
// which take the forms "443 ssl", "0.0.0.0:443", "[::]:443 ssl" and "*:443".
// A udp listen never competes with the TCP router and a unix: socket has no
// port at all; both report false.
func nginxListenSocket(arguments string) (listenSocket, bool) {
	fields := strings.Fields(arguments)
	if len(fields) == 0 {
		return listenSocket{}, false
	}
	for _, option := range fields[1:] {
		if option == "udp" {
			return listenSocket{}, false
		}
	}
	address, port := "", fields[0]
	if index := strings.LastIndex(port, ":"); index >= 0 {
		address, port = port[:index], port[index+1:]
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return listenSocket{}, false
	}
	return listenSocket{address: nginxroute.NormalizeListenAddress(address), port: uint16(number)}, true
}

func streamServerBindsSocket(server nginxStreamServer, socket listenSocket) bool {
	for _, listen := range server.listens {
		if bound, ok := nginxListenSocket(listen); ok && bound == socket {
			return true
		}
	}
	return false
}

// managedListenSockets reads the sockets the compiled router configuration
// binds.
func managedListenSockets(configuration string) []listenSocket {
	var sockets []listenSocket
	seen := map[listenSocket]bool{}
	for _, raw := range strings.Split(configuration, "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if !strings.HasPrefix(line, "listen ") {
			continue
		}
		socket, ok := nginxListenSocket(strings.TrimSuffix(strings.TrimPrefix(line, "listen "), ";"))
		if !ok || seen[socket] {
			continue
		}
		seen[socket] = true
		sockets = append(sockets, socket)
	}
	return sockets
}

// foreignStreamConflicts names the stream servers still holding a socket the
// router needs after takeOverManagedPorts has run. Reaching this means the
// block could not be moved aside, so the deploy reports it rather than letting
// `nginx -t` reject the merged configuration and roll everything back.
func foreignStreamConflicts(ctx context.Context, configuration string) []string {
	sockets := managedListenSockets(configuration)
	if len(sockets) == 0 {
		return nil
	}
	dump, err := exec.CommandContext(ctx, "nginx", "-T").CombinedOutput()
	if err != nil {
		return nil
	}
	servers := nginxForeignStreamServers(string(dump))
	var conflicts []string
	reported := map[string]bool{}
	for _, socket := range sockets {
		for _, server := range servers {
			if !streamServerBindsSocket(server, socket) {
				continue
			}
			message := "TCP/" + strconv.Itoa(int(socket.port)) + " 已被 Nginx 配置文件 " + server.file + " 中的 stream 服务占用"
			if reported[message] {
				continue
			}
			reported[message] = true
			conflicts = append(conflicts, message)
		}
	}
	return conflicts
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
		if socket, ok := nginxListenSocket(listen); ok && socket.port == port {
			return true
		}
	}
	return false
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
		socket, ok := nginxListenSocket(arguments)
		if !ok || socket.port != from {
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
