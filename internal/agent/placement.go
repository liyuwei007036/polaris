package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/liyuwei007036/polaris/internal/nginxroute"
)

// Where a service actually listens is decided here, on the node, and not by
// the control plane. The control plane knows which public port an operator
// asked for; only the node can see what is already on that port — another
// service of its own, or an Nginx the operator installed long before polaris.
// Deciding centrally means guessing at that, and a wrong guess is a service
// that can never start.

// placementPlan is what the node decided for one compiled configuration: the
// configuration to actually write, the SNI routes the managed Nginx has to
// serve for it, and a sentence per moved service for the operator.
type placementPlan struct {
	configuration string
	routes        []nginxroute.Route
	summaries     []string
	// directPorts are the TCP ports inbounds bind themselves. Nginx must not
	// hold any of them when sing-box starts.
	directPorts map[uint16]bool
}

// routesExcept drops the routes whose public port is in the given set. It is
// how the router gives up a port before sing-box binds it directly.
func (plan placementPlan) routesExcept(ports map[uint16]bool) []nginxroute.Route {
	kept := make([]nginxroute.Route, 0, len(plan.routes))
	for _, route := range plan.routes {
		if ports[route.Port] {
			continue
		}
		kept = append(kept, route)
	}
	return kept
}

// planNodePlacement reads the host and decides placement for a compiled
// configuration.
//
// Which ports count as taken is the delicate part. A port Nginx holds only
// because of polaris's own router is not taken — otherwise a service could
// never move back to binding it directly once it is alone again. A port with a
// takeover record is taken even so: a site of the operator's lives on loopback
// behind the router there, and giving the port up would take it offline.
func planNodePlacement(ctx context.Context, configuration string, names map[string]string, dataDir string) (placementPlan, error) {
	sockets, _ := listeningSockets(ctx)
	occupied := occupiedTCPPorts(sockets)
	current, err := os.ReadFile(managedNginxConfig)
	if err == nil {
		for _, port := range managedListenPorts(string(current)) {
			delete(occupied, port)
		}
	}
	for _, record := range loadNginxTakeover(dataDir) {
		occupied[record.Port] = true
	}
	reserved := map[uint16]bool{}
	existing := existingSingBoxBackendPorts(managedSingBoxConfig)
	return planListenerPlacementWithExisting(configuration, names, occupied, existing, func() (uint16, bool) {
		return freeLoopbackPortFrom(singBoxBackendPortFirst, singBoxBackendPortLast, sockets, reserved)
	})
}

func existingSingBoxBackendPorts(path string) map[string]uint16 {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var parsed struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	ports := make(map[string]uint16)
	for _, raw := range parsed.Inbounds {
		tag, _ := raw["tag"].(string)
		listen, _ := raw["listen"].(string)
		if tag == "" || (listen != "127.0.0.1" && listen != "::1") {
			continue
		}
		var port uint16
		switch v := raw["listen_port"].(type) {
		case float64:
			port = uint16(v)
		case json.Number:
			if n, err := strconv.ParseUint(v.String(), 10, 16); err == nil {
				port = uint16(n)
			}
		}
		if port >= singBoxBackendPortFirst && port < singBoxBackendPortLast {
			ports[tag] = port
		}
	}
	return ports
}

// Backends for services behind the router are handed out below the range the
// Nginx takeover uses, so the two allocators cannot collide on a host where
// both run.
const (
	singBoxBackendPortFirst = 20000
	singBoxBackendPortLast  = 30000
)

// inboundBinding is the part of a compiled inbound placement looks at.
type inboundBinding struct {
	inbound map[string]any
	tag     string
	address string
	port    uint16
	sni     string
}

// planListenerPlacement decides how each inbound of a compiled configuration
// binds its public port, and rewrites the configuration to match.
//
// An inbound alone on its port binds it directly, so sing-box sees the client's
// own address rather than a loopback one — the router cannot carry a source
// address across, and for Reality no protocol layer is left that could. It
// moves behind the managed SNI router only when something else needs the same
// socket: another inbound on that port, or a process already listening there.
//
// names maps an inbound tag to the TLS name the router matches it by. It
// travels beside the configuration rather than inside it: sing-box gives
// tls.server_name its own meaning on an inbound, and borrowing that field to
// carry routing information would change how the service answers its clients.
//
// allocate hands out a free loopback port; it is a parameter so the decision
// can be exercised without touching the host.
func planListenerPlacement(configuration string, names map[string]string, occupied map[uint16]bool, allocate func() (uint16, bool)) (placementPlan, error) {
	return planListenerPlacementWithExisting(configuration, names, occupied, nil, allocate)
}

func planListenerPlacementWithExisting(configuration string, names map[string]string, occupied map[uint16]bool, existing map[string]uint16, allocate func() (uint16, bool)) (placementPlan, error) {
	decoder := json.NewDecoder(strings.NewReader(configuration))
	// Numbers stay exactly as the control plane wrote them; re-encoding through
	// float64 would rewrite anything large into scientific notation.
	decoder.UseNumber()
	var parsed map[string]any
	if err := decoder.Decode(&parsed); err != nil {
		return placementPlan{}, errors.New("解析 sing-box 配置失败：" + err.Error())
	}
	rawInbounds, _ := parsed["inbounds"].([]any)
	byPort := map[uint16][]inboundBinding{}
	var ports []uint16
	for _, raw := range rawInbounds {
		inbound, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		binding, ok := readInboundBinding(inbound, names)
		if !ok {
			continue
		}
		if _, seen := byPort[binding.port]; !seen {
			ports = append(ports, binding.port)
		}
		byPort[binding.port] = append(byPort[binding.port], binding)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })

	plan := placementPlan{directPorts: map[uint16]bool{}}
	reservedPorts := map[uint16]bool{}
	for _, port := range ports {
		group := byPort[port]
		if len(group) == 1 && !occupied[port] {
			plan.directPorts[port] = true
			continue
		}
		names := map[string]bool{}
		for _, binding := range group {
			if binding.sni == "" || !nginxroute.ValidSNI(binding.sni) {
				return placementPlan{}, errors.New(placementBlockedMessage(port, binding, len(group), occupied[port]))
			}
			if names[binding.sni] {
				return placementPlan{}, errors.New("端口 " + strconv.Itoa(int(port)) + " 上有多个接入服务使用了相同的连接域名 " + binding.sni + "，无法按域名分流；请改用不同的连接域名，或为其中一个改用其他端口")
			}
			names[binding.sni] = true
		}
		defaultAssigned := false
		for _, binding := range group {
			var backendPort uint16
			if existingPort := existing[binding.tag]; existingPort >= singBoxBackendPortFirst && existingPort < singBoxBackendPortLast && !reservedPorts[existingPort] {
				backendPort = existingPort
				reservedPorts[existingPort] = true
			} else {
				p, ok := allocate()
				if !ok {
					return placementPlan{}, errors.New("没有可用的本机端口用于共用 " + strconv.Itoa(int(port)) + " 端口")
				}
				backendPort = p
				reservedPorts[backendPort] = true
			}
			binding.inbound["listen"] = "127.0.0.1"
			binding.inbound["listen_port"] = json.Number(strconv.Itoa(int(backendPort)))
			binding.inbound["proxy_protocol"] = true

			isDefault := false
			if !defaultAssigned && isInboundReality(binding.inbound) {
				isDefault = true
				defaultAssigned = true
			}

			plan.routes = append(plan.routes, nginxroute.Route{
				ListenAddress: binding.address, Port: port, SNI: binding.sni,
				BackendAddress: "127.0.0.1", BackendPort: backendPort,
				IsDefault:      isDefault,
			})
			plan.summaries = append(plan.summaries,
				binding.sni+" 与该端口上的其他服务共用 "+strconv.Itoa(int(port))+"，已改为 127.0.0.1:"+strconv.Itoa(int(backendPort))+" 并由 Nginx 按域名分流")
		}
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(parsed); err != nil {
		return placementPlan{}, errors.New("生成 sing-box 配置失败：" + err.Error())
	}
	plan.configuration = encoded.String()
	return plan, nil
}

// placementBlockedMessage explains why an inbound can neither keep the port nor
// be routed off it. Both reasons name what the operator has to change, because
// nothing polaris can do on its own resolves either one.
func placementBlockedMessage(port uint16, binding inboundBinding, groupSize int, occupied bool) string {
	reason := "端口 " + strconv.Itoa(int(port)) + " 上有多个接入服务"
	if groupSize == 1 && occupied {
		reason = "端口 " + strconv.Itoa(int(port)) + " 已被服务器上的其他程序占用"
	}
	name := binding.tag
	if name == "" {
		name = "该接入服务"
	}
	return reason + "，需要按 TLS 域名(SNI)分流，但 " + name + " 没有可用的域名；请为它启用 TLS 并填写连接域名，或为它改用其他端口"
}

// readInboundBinding reports the public TCP socket an inbound asks for. A UDP
// inbound is skipped: the SNI router is TCP-only, so a UDP service never
// competes with it even on the same port number. So is an inbound already on
// loopback, which is not public to begin with.
func readInboundBinding(inbound map[string]any, names map[string]string) (inboundBinding, bool) {
	inboundType, _ := inbound["type"].(string)
	if inboundNetwork(inboundType) != "tcp" {
		return inboundBinding{}, false
	}
	port, ok := jsonPort(inbound["listen_port"])
	if !ok {
		return inboundBinding{}, false
	}
	address, _ := inbound["listen"].(string)
	switch strings.TrimSuffix(strings.TrimPrefix(address, "["), "]") {
	case "", "*", "0.0.0.0", "::":
		address = "0.0.0.0"
	default:
		return inboundBinding{}, false
	}
	tag, _ := inbound["tag"].(string)
	sni := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(names[tag])), ".")
	return inboundBinding{inbound: inbound, tag: tag, address: address, port: port, sni: sni}, true
}

func jsonPort(value any) (uint16, bool) {
	var text string
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	case float64:
		text = strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return 0, false
	}
	port, err := strconv.ParseUint(text, 10, 16)
	if err != nil || port == 0 {
		return 0, false
	}
	return uint16(port), true
}

// occupiedTCPPorts reports the TCP ports held by something other than sing-box
// itself. sing-box holds the ports of the configuration being replaced and
// gives them up when it restarts, so its own sockets must never count.
func occupiedTCPPorts(sockets []listeningSocket) map[uint16]bool {
	occupied := map[uint16]bool{}
	for _, socket := range sockets {
		if socket.network != "tcp" || socket.process == "sing-box" {
			continue
		}
		occupied[socket.port] = true
	}
	return occupied
}

func isInboundReality(inbound map[string]any) bool {
	tls, ok := inbound["tls"].(map[string]any)
	if !ok {
		return false
	}
	reality, ok := tls["reality"].(map[string]any)
	if !ok {
		return false
	}
	enabled, _ := reality["enabled"].(bool)
	return enabled
}
