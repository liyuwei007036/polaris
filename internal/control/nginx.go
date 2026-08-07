package control

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/liyuwei007036/polaris/internal/nginxroute"
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
	// ListenerEnabled mirrors the backing listener's state. A route reaches
	// the compiled Nginx configuration only when both are enabled.
	ListenerEnabled bool `json:"listener_enabled"`
}

// CompileNginxStream produces an isolated stream block. The system Nginx
// configuration must include /etc/nginx/stream-conf.d/*.conf inside stream {}.
func CompileNginxStream(routes []IngressRoute) (string, string, error) {
	compiledRoutes := make([]nginxroute.Route, 0, len(routes))
	for _, route := range routes {
		compiledRoutes = append(compiledRoutes, nginxroute.Route{
			ListenAddress: route.ListenAddress, Port: route.Port, SNI: route.SNI,
			BackendAddress: route.BackendAddress, BackendPort: route.BackendPort,
		})
	}
	configuration, err := nginxroute.Compile(compiledRoutes)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256([]byte(configuration))
	return configuration, hex.EncodeToString(digest[:]), nil
}

func validSNI(value string) bool {
	return nginxroute.ValidSNI(value)
}
