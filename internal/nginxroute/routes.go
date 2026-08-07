package nginxroute

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

type Route struct {
	ListenAddress  string
	Port           uint16
	SNI            string
	BackendAddress string
	BackendPort    uint16
}

type routeKey struct {
	address string
	port    uint16
}

func Compile(routes []Route) (string, error) {
	groups, keys, err := groupRoutes(routes)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	for _, key := range keys {
		name := GroupName(key.address, key.port)
		output.WriteString("map $ssl_preread_server_name $")
		output.WriteString(name)
		output.WriteString(" {\n    default \"127.0.0.1:1\";\n")
		output.WriteString(Marker(name))
		for _, route := range groups[key] {
			writeMapRoute(&output, route)
		}
		output.WriteString("}\nserver {\n    listen ")
		output.WriteString(net.JoinHostPort(key.address, strconv.Itoa(int(key.port))))
		output.WriteString(";\n    ssl_preread on;\n    proxy_pass $")
		output.WriteString(name)
		output.WriteString(";\n    proxy_connect_timeout 10s;\n    proxy_timeout 10m;\n}\n\n")
	}
	return output.String(), nil
}

func MergePassthrough(configuration string, routes []Route) (string, error) {
	groups, keys, err := groupRoutes(routes)
	if err != nil {
		return "", err
	}
	for _, key := range keys {
		name := GroupName(key.address, key.port)
		marker := Marker(name)
		markerIndex := strings.Index(configuration, marker)
		if markerIndex < 0 || strings.Index(configuration[markerIndex+len(marker):], marker) >= 0 {
			return "", fmt.Errorf("managed Nginx route group %s is missing or ambiguous", net.JoinHostPort(key.address, strconv.Itoa(int(key.port))))
		}
		blockEnd := strings.Index(configuration[markerIndex+len(marker):], "}\nserver {")
		if blockEnd < 0 {
			return "", fmt.Errorf("managed Nginx route group %s is malformed", net.JoinHostPort(key.address, strconv.Itoa(int(key.port))))
		}
		block := configuration[markerIndex+len(marker) : markerIndex+len(marker)+blockEnd]
		var additions strings.Builder
		for _, route := range groups[key] {
			if strings.Contains(block, "    "+route.SNI+" ") {
				return "", fmt.Errorf("passthrough SNI %s conflicts with a managed route", route.SNI)
			}
			writeMapRoute(&additions, route)
		}
		configuration = configuration[:markerIndex+len(marker)] + additions.String() + configuration[markerIndex+len(marker):]
	}
	return configuration, nil
}

func GroupName(address string, port uint16) string {
	return "polaris_" + strings.NewReplacer(".", "_", ":", "_").Replace(address) + "_" + strconv.Itoa(int(port))
}

func Marker(groupName string) string {
	return "    # polaris-passthrough:" + groupName + "\n"
}

func groupRoutes(routes []Route) (map[routeKey][]Route, []routeKey, error) {
	groups := make(map[routeKey][]Route)
	seen := make(map[string]struct{})
	for _, route := range routes {
		route.SNI = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(route.SNI)), ".")
		if err := Validate(route); err != nil {
			return nil, nil, err
		}
		key := routeKey{address: route.ListenAddress, port: route.Port}
		identity := strings.ToLower(route.ListenAddress + ":" + strconv.Itoa(int(route.Port)) + ":" + route.SNI)
		if _, exists := seen[identity]; exists {
			return nil, nil, fmt.Errorf("duplicate SNI route for %s", route.SNI)
		}
		seen[identity] = struct{}{}
		groups[key] = append(groups[key], route)
	}
	keys := make([]routeKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
		sort.Slice(groups[key], func(i, j int) bool { return groups[key][i].SNI < groups[key][j].SNI })
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].address == keys[j].address {
			return keys[i].port < keys[j].port
		}
		return keys[i].address < keys[j].address
	})
	return groups, keys, nil
}

func Validate(route Route) error {
	if route.Port == 0 || route.BackendPort == 0 || route.SNI == "" {
		return errors.New("route address, ports, and SNI are required")
	}
	if net.ParseIP(route.ListenAddress) == nil || net.ParseIP(route.BackendAddress) == nil {
		return errors.New("route addresses must be IP addresses")
	}
	if !ValidSNI(route.SNI) {
		return errors.New("route SNI is invalid")
	}
	return nil
}

func ValidSNI(value string) bool {
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

func writeMapRoute(output *strings.Builder, route Route) {
	output.WriteString("    ")
	output.WriteString(route.SNI)
	output.WriteString(" \"")
	output.WriteString(net.JoinHostPort(route.BackendAddress, strconv.Itoa(int(route.BackendPort))))
	output.WriteString("\";\n")
}
