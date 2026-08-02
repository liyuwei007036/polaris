package control

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

type IngressRoute struct {
	ID             string `json:"id,omitempty"`
	NodeID         string `json:"node_id,omitempty"`
	ListenerID     string `json:"listener_id,omitempty"`
	ListenAddress  string `json:"listen_address"`
	Port           uint16 `json:"port"`
	SNI            string `json:"sni"`
	BackendAddress string `json:"backend_address"`
	BackendPort    uint16 `json:"backend_port"`
	Enabled        bool   `json:"enabled"`
}

// CompileNginxStream produces an isolated stream block. The system Nginx
// configuration must include /etc/nginx/stream-conf.d/*.conf inside stream {}.
func CompileNginxStream(routes []IngressRoute) (string, string, error) {
	type routeKey struct {
		address string
		port    uint16
	}
	groups := map[routeKey][]IngressRoute{}
	seenSNI := map[string]struct{}{}
	for _, route := range routes {
		if route.Port == 0 || route.BackendPort == 0 || route.SNI == "" {
			return "", "", errors.New("route address, ports, and SNI are required")
		}
		if net.ParseIP(route.ListenAddress) == nil || net.ParseIP(route.BackendAddress) == nil {
			return "", "", errors.New("route addresses must be IP addresses")
		}
		if !validSNI(route.SNI) {
			return "", "", errors.New("route SNI is invalid")
		}
		key := strings.ToLower(route.ListenAddress + ":" + strconv.Itoa(int(route.Port)) + ":" + route.SNI)
		if _, exists := seenSNI[key]; exists {
			return "", "", fmt.Errorf("duplicate SNI route for %s", route.SNI)
		}
		seenSNI[key] = struct{}{}
		groups[routeKey{route.ListenAddress, route.Port}] = append(groups[routeKey{route.ListenAddress, route.Port}], route)
	}
	keys := make([]routeKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].address == keys[j].address {
			return keys[i].port < keys[j].port
		}
		return keys[i].address < keys[j].address
	})
	var output strings.Builder
	for _, key := range keys {
		routes := groups[key]
		sort.Slice(routes, func(i, j int) bool { return routes[i].SNI < routes[j].SNI })
		name := "sb_control_" + strings.NewReplacer(".", "_", ":", "_").Replace(key.address) + "_" + strconv.Itoa(int(key.port))
		output.WriteString("map $ssl_preread_server_name $")
		output.WriteString(name)
		output.WriteString(" {\n    default \"127.0.0.1:1\";\n")
		for _, route := range routes {
			output.WriteString("    ")
			output.WriteString(route.SNI)
			output.WriteString(" \"")
			output.WriteString(route.BackendAddress)
			output.WriteString(":")
			output.WriteString(strconv.Itoa(int(route.BackendPort)))
			output.WriteString("\";\n")
		}
		output.WriteString("}\nserver {\n    listen ")
		output.WriteString(key.address)
		output.WriteString(":")
		output.WriteString(strconv.Itoa(int(key.port)))
		output.WriteString(";\n    ssl_preread on;\n    proxy_pass $")
		output.WriteString(name)
		output.WriteString(";\n}\n\n")
	}
	configuration := output.String()
	digest := sha256.Sum256([]byte(configuration))
	return configuration, hex.EncodeToString(digest[:]), nil
}

func validSNI(value string) bool {
	value = strings.TrimSuffix(strings.ToLower(value), ".")
	if value == "" || len(value) > 253 || net.ParseIP(value) != nil {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' {
				return false
			}
		}
	}
	return true
}
