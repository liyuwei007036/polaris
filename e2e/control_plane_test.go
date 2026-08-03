//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	e2eAdminEmail    = "admin.e2e@example.test"
	e2eAdminPassword = "E2E-Admin-Password-2026!"
)

// TestControlPlaneProcessJourneyWithRealAgent runs the compiled Master and
// Agent as independent product processes. Host integrations such as sing-box
// and systemctl are deterministic stubs; the HTTP and Noise control planes are
// real sockets between the two product processes.
func TestControlPlaneProcessJourneyWithRealAgent(t *testing.T) {
	root := repositoryRoot(t)
	work := t.TempDir()
	binaryPath := buildProgram(t, root, "./cmd/sb-control", filepath.Join(work, executableName("sb-control-e2e")))
	stubDir := filepath.Join(work, "command-stubs")
	if err := os.MkdirAll(stubDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stubPath := buildProgram(t, root, "./e2e/cmdstub", filepath.Join(stubDir, executableName("sing-box")))
	for _, command := range []string{"systemctl", "nginx", "nft", "fail2ban-client"} {
		copyExecutable(t, stubPath, filepath.Join(stubDir, executableName(command)))
	}

	masterData := filepath.Join(work, "master-data")
	agentData := filepath.Join(work, "agent-data")
	managedRoot := filepath.Join(work, "managed-root")
	commandLog := filepath.Join(work, "commands.log")
	clashAPI := startClashAPI(t)

	secret := initializeAdministrator(t, binaryPath, masterData)
	masterPublicKey := commandLastField(t, binaryPath, "master", "show-pubkey", "--data-dir", masterData)
	agentPort := freeTCPPort(t)
	browserPort := freeTCPPort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", browserPort)
	masterAddress := fmt.Sprintf("127.0.0.1:%d", agentPort)

	master := startProcess(t, "master", binaryPath, nil,
		"master", "serve", "--data-dir", masterData,
		"--agent-port", fmt.Sprint(agentPort),
		"--web-port", fmt.Sprint(browserPort),
		"--allow-insecure-http")
	waitFor(t, 15*time.Second, "master health endpoint", func() bool {
		response, err := http.Get(baseURL + "/api/v1/health")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	})
	t.Cleanup(func() { master.stop(t) })

	verifyEmbeddedWebApplication(t, baseURL)
	api := newAPIClient(t, baseURL)
	api.login(t, secret)
	verifyUnauthenticatedRequestIsRejected(t, baseURL)

	var tokenResponse struct {
		Token string `json:"token"`
	}
	api.mustJSON(t, http.MethodPost, "/api/v1/nodes/registration-tokens", map[string]any{"lifetime_seconds": 600}, true, http.StatusCreated, &tokenResponse)
	if tokenResponse.Token == "" {
		t.Fatal("registration token is empty")
	}

	registrationOutput := runCommand(t, 20*time.Second, nil, binaryPath,
		"agent", "register", "--data-dir", agentData,
		"--master", masterAddress, "--master-pubkey", masterPublicKey,
		"--token", tokenResponse.Token)
	registrationMatch := regexp.MustCompile(`registration_id=([a-f0-9]{32})`).FindStringSubmatch(registrationOutput)
	if len(registrationMatch) != 2 {
		t.Fatalf("agent registration did not return a pending registration ID:\n%s", registrationOutput)
	}
	registrationID := registrationMatch[1]

	var registrations struct {
		Registrations []struct {
			ID string `json:"id"`
		} `json:"registrations"`
	}
	api.mustJSON(t, http.MethodGet, "/api/v1/registrations", nil, true, http.StatusOK, &registrations)
	if !registrationExists(registrations.Registrations, registrationID) {
		t.Fatalf("pending registration %s is not visible through the API", registrationID)
	}
	var approval struct {
		NodeID string `json:"node_id"`
		Status string `json:"status"`
	}
	api.mustJSON(t, http.MethodPost, "/api/v1/nodes/"+registrationID+"/approve", map[string]any{}, true, http.StatusOK, &approval)
	if approval.NodeID == "" || approval.Status != "approved" {
		t.Fatalf("unexpected approval response: %#v", approval)
	}

	agentEnv := append(os.Environ(),
		"SB_CONTROL_E2E_ROOT="+managedRoot,
		"SB_CONTROL_E2E_COMMAND_LOG="+commandLog,
		"SB_CONTROL_E2E_CLASH_API_URL="+clashAPI.URL,
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	agentProcess := startProcess(t, "agent", binaryPath, agentEnv,
		"agent", "serve", "--data-dir", agentData,
		"--master", masterAddress, "--master-pubkey", masterPublicKey,
		"--heartbeat-interval", "5s", "--connections-interval", "1s")
	if master.command.Process == nil || agentProcess.command.Process == nil || master.command.Process.Pid == agentProcess.command.Process.Pid {
		t.Fatal("Master and Agent were not started as distinct product processes")
	}
	t.Logf("real product processes started: master_pid=%d agent_pid=%d; host command dependencies use stubs", master.command.Process.Pid, agentProcess.command.Process.Pid)
	t.Cleanup(func() { agentProcess.stop(t) })
	waitFor(t, 20*time.Second, "approved agent to report online", func() bool {
		var result struct {
			Nodes []struct {
				ID           string `json:"id"`
				Online       bool   `json:"online"`
				Architecture string `json:"architecture"`
			} `json:"nodes"`
		}
		if !api.tryJSON(http.MethodGet, "/api/v1/nodes", nil, false, http.StatusOK, &result) {
			return false
		}
		for _, node := range result.Nodes {
			if node.ID == approval.NodeID && node.Online && node.Architecture != "" {
				return true
			}
		}
		return false
	})
	waitFor(t, 10*time.Second, "agent connection snapshot to reach the master", func() bool {
		var result struct {
			Connections []struct {
				ID             string   `json:"id"`
				Host           string   `json:"host"`
				SourceLocation string   `json:"source_location"`
				RulePayload    string   `json:"rule_payload"`
				Chains         []string `json:"chains"`
			} `json:"connections"`
		}
		if !api.tryJSON(http.MethodGet, "/api/v1/nodes/"+approval.NodeID+"/connections", nil, false, http.StatusOK, &result) {
			return false
		}
		return len(result.Connections) == 1 && result.Connections[0].ID == "e2e-connection" && result.Connections[0].Host == "example.test" &&
			result.Connections[0].SourceLocation == "内网" && result.Connections[0].RulePayload == "example.test" && len(result.Connections[0].Chains) == 1
	})

	var outbound struct {
		ID string `json:"id"`
	}
	rejectingProxyPort := rejectingTCPPort(t)
	api.mustJSON(t, http.MethodPost, "/api/v1/outbounds", map[string]any{
		"name": "E2E 拒绝连接出口", "type": "socks", "server": "127.0.0.1", "server_port": rejectingProxyPort, "enabled": true,
	}, true, http.StatusCreated, &outbound)
	if outbound.ID == "" {
		t.Fatal("managed outbound ID is empty")
	}
	var probeTask taskResponse
	api.mustJSON(t, http.MethodPost, "/api/v1/outbounds/"+outbound.ID+"/test", map[string]any{"node_id": approval.NodeID}, true, http.StatusAccepted, &probeTask)
	probeTask = waitForTask(t, api, probeTask.ID, 20*time.Second)
	if probeTask.Status != "failed" || probeTask.ResultSummary == "" {
		t.Fatalf("real agent did not execute and report the expected failed outbound probe: %#v", probeTask)
	}
	assertFileExists(t, filepath.Join(agentData, "completed-tasks", probeTask.ID+".json"))

	api.mustJSON(t, http.MethodPut, "/api/v1/nodes/"+approval.NodeID+"/client-address", map[string]any{"address": "e2e.example.test"}, true, http.StatusOK, nil)
	var quickListener quickListenerResponse
	header := api.mustJSON(t, http.MethodPost, "/api/v1/listeners/quick", map[string]any{
		"listener": map[string]any{
			"node_id": approval.NodeID, "name": "E2E VLESS", "listen_address": "0.0.0.0", "port": 24443, "enabled": true,
			"spec": map[string]any{"protocol": "vless", "network": "tcp"},
		},
		"accounts": []map[string]any{
			{"name": "E2E 用户 A", "outbound_id": "direct"},
			{"name": "E2E 用户 B", "outbound_id": outbound.ID},
		},
	}, true, http.StatusCreated, &quickListener)
	if quickListener.Listener.ID == "" || len(quickListener.Endpoints) != 2 {
		t.Fatalf("quick listener did not create one listener and two users: %#v", quickListener)
	}
	applyTaskID := header.Get("X-SB-Auto-Apply-Task")
	if applyTaskID == "" {
		t.Fatal("quick listener response did not expose its automatic apply task")
	}
	applyTask := waitForTask(t, api, applyTaskID, 20*time.Second)
	if applyTask.Status != "succeeded" {
		t.Fatalf("real agent did not complete automatic sing-box application: %#v", applyTask)
	}
	configurationPath := filepath.Join(managedRoot, "etc", "sing-box", "config.json")
	configuration := readFile(t, configurationPath)
	assertAuthenticatedUserRoute(t, configuration, "E2E 用户 B", "outbound-"+outbound.ID)
	commandInvocations := readFile(t, commandLog)
	for _, expected := range []string{"sing-box check -c", "systemctl restart sing-box.service", "systemctl is-active --quiet sing-box.service"} {
		if !strings.Contains(commandInvocations, expected) {
			t.Fatalf("agent did not invoke %q; command log:\n%s", expected, commandInvocations)
		}
	}

	certificatePEM, privateKeyPEM := selfSignedCertificate(t, []string{"vless-a.e2e.test", "vless-b.e2e.test", "hy2.e2e.test"})
	var certificate struct {
		ID string `json:"id"`
	}
	api.mustJSON(t, http.MethodPost, "/api/v1/certificates", map[string]any{
		"name": "E2E 共享证书", "certificate_pem": certificatePEM, "private_key_pem": privateKeyPEM, "enabled": true,
	}, true, http.StatusCreated, &certificate)
	sharedEndpointIDs := make([]string, 0, 3)
	for index, serverName := range []string{"vless-a.e2e.test", "vless-b.e2e.test"} {
		var shared quickListenerResponse
		headers := api.mustJSON(t, http.MethodPost, "/api/v1/listeners/quick", map[string]any{
			"listener": map[string]any{
				"node_id": approval.NodeID, "name": fmt.Sprintf("E2E 自动端口 VLESS %d", index+1), "listen_address": "0.0.0.0", "port": 443, "enabled": true,
				"spec": map[string]any{
					"protocol": "vless", "network": "tcp",
					"tls": map[string]any{"enabled": true, "server_name": serverName, "certificate_id": certificate.ID},
				},
			},
			"accounts": []map[string]any{{"name": fmt.Sprintf("E2E 接入用户 %d", index+1), "outbound_id": "direct"}},
		}, true, http.StatusCreated, &shared)
		waitForSuccessfulTaskHeader(t, api, headers, "X-SB-Auto-Apply-Task")
		if index > 0 {
			waitForSuccessfulTaskHeader(t, api, headers, "X-SB-Auto-Apply-Nginx-Task")
		} else if headers.Get("X-SB-Auto-Apply-Nginx-Task") != "" {
			t.Fatal("the first listener unexpectedly enabled automatic TCP port routing")
		}
		sharedEndpointIDs = append(sharedEndpointIDs, shared.Endpoints[0].ID)
	}
	nginxConfiguration := readFile(t, filepath.Join(managedRoot, "etc", "nginx", "stream-conf.d", "sb-control.conf"))
	for _, serverName := range []string{"vless-a.e2e.test", "vless-b.e2e.test"} {
		if !strings.Contains(nginxConfiguration, serverName) {
			t.Fatalf("Nginx automatic port configuration is missing %s:\n%s", serverName, nginxConfiguration)
		}
	}

	var hysteria2 quickListenerResponse
	hysteriaHeaders := api.mustJSON(t, http.MethodPost, "/api/v1/listeners/quick", map[string]any{
		"listener": map[string]any{
			"node_id": approval.NodeID, "name": "E2E Hysteria2 UDP 443", "listen_address": "0.0.0.0", "port": 443, "enabled": true,
			"spec": map[string]any{
				"protocol": "hysteria2", "network": "udp",
				"tls": map[string]any{"enabled": true, "server_name": "hy2.e2e.test", "certificate_id": certificate.ID},
			},
		},
		"accounts": []map[string]any{{"name": "E2E Hysteria2 用户", "outbound_id": "direct"}},
	}, true, http.StatusCreated, &hysteria2)
	waitForSuccessfulTaskHeader(t, api, hysteriaHeaders, "X-SB-Auto-Apply-Task")
	sharedEndpointIDs = append(sharedEndpointIDs, hysteria2.Endpoints[0].ID)
	configuration = readFile(t, configurationPath)
	if !strings.Contains(configuration, `"type": "hysteria2"`) {
		t.Fatalf("same-numbered UDP 443 Hysteria2 listener is missing from applied configuration:\n%s", configuration)
	}

	api.mustJSON(t, http.MethodPost, "/api/v1/nodes/"+approval.NodeID+"/firewall/rules", map[string]any{
		"action": "accept", "protocol": "tcp", "cidr": "192.0.2.0/24", "port": 443, "enabled": true,
	}, true, http.StatusCreated, nil)
	var firewallTask taskResponse
	api.mustJSON(t, http.MethodPost, "/api/v1/nodes/"+approval.NodeID+"/firewall/publish", map[string]any{}, true, http.StatusAccepted, &firewallTask)
	if completed := waitForTask(t, api, firewallTask.ID, 20*time.Second); completed.Status != "succeeded" {
		t.Fatalf("firewall task did not succeed: %#v", completed)
	}

	api.mustJSON(t, http.MethodPost, "/api/v1/nodes/"+approval.NodeID+"/fail2ban/jails", map[string]any{
		"name": "e2e-auth", "log_path": "/var/log/sing-box/access.log", "filter_name": "e2e-auth",
		"fail_regex": "^.*authentication failed.*$", "max_retry": 3, "find_time_seconds": 600, "ban_time_seconds": 3600, "enabled": true,
	}, true, http.StatusCreated, nil)
	var fail2banTask taskResponse
	api.mustJSON(t, http.MethodPost, "/api/v1/nodes/"+approval.NodeID+"/fail2ban/publish", map[string]any{}, true, http.StatusAccepted, &fail2banTask)
	if completed := waitForTask(t, api, fail2banTask.ID, 20*time.Second); completed.Status != "succeeded" {
		t.Fatalf("Fail2Ban task did not succeed: %#v", completed)
	}
	assertFileExists(t, filepath.Join(managedRoot, "etc", "fail2ban", "jail.d", "sb-control.local"))
	commandInvocations = readFile(t, commandLog)
	for _, expected := range []string{
		"nginx -t", "systemctl reload nginx.service", "nft -c -f", "nft -f",
		"fail2ban-client -t", "systemctl reload-or-restart fail2ban.service",
	} {
		if !strings.Contains(commandInvocations, expected) {
			t.Fatalf("agent did not invoke %q; command log:\n%s", expected, commandInvocations)
		}
	}

	endpointIDs := append([]string{quickListener.Endpoints[0].ID, quickListener.Endpoints[1].ID}, sharedEndpointIDs...)
	var proxyGroup struct {
		ID string `json:"id"`
	}
	api.mustJSON(t, http.MethodPost, "/api/v1/mihomo/proxy-groups", map[string]any{
		"name": "全部节点", "strategy": "select",
		"members": func() []map[string]string {
			members := make([]map[string]string, 0, len(endpointIDs))
			for _, endpointID := range endpointIDs {
				members = append(members, map[string]string{"kind": "endpoint", "id": endpointID})
			}
			return members
		}(),
	}, true, http.StatusCreated, &proxyGroup)
	var clientConfig struct {
		ID string `json:"id"`
	}
	api.mustJSON(t, http.MethodPost, "/api/v1/mihomo/client-configs", map[string]any{
		"name": "E2E 客户端", "proxy_group_ids": []string{proxyGroup.ID},
		"rule_mode": "table", "rule_providers": []map[string]any{{
			"name": "远程代理规则", "behavior": "domain", "format": "mrs",
			"url": "https://rules.example.test/proxy.mrs", "path": "./ruleset/proxy.mrs",
			"interval": 86400, "proxy": "DIRECT",
		}},
		"rules": []map[string]any{
			{"type": "RULE-SET", "value": "远程代理规则", "action": "全部节点"},
			{"type": "GEOSITE", "value": "CN", "action": "DIRECT"},
			{"type": "GEOIP", "value": "CN", "action": "DIRECT", "no_resolve": true},
			{"type": "MATCH", "action": "全部节点"},
		},
	}, true, http.StatusCreated, &clientConfig)
	var subscription struct {
		Path string `json:"subscription_path"`
	}
	api.mustJSON(t, http.MethodPost, "/api/v1/mihomo/client-configs/"+clientConfig.ID+"/subscription/rotate", map[string]any{}, true, http.StatusOK, &subscription)
	status, responseHeader, yaml := rawRequest(t, http.DefaultClient, http.MethodGet, baseURL+subscription.Path, nil, "")
	if status != http.StatusOK || !strings.Contains(responseHeader.Get("Content-Type"), "application/yaml") {
		t.Fatalf("Mihomo subscription returned status=%d content-type=%q", status, responseHeader.Get("Content-Type"))
	}
	if disposition := responseHeader.Get("Content-Disposition"); !strings.Contains(disposition, `filename*=UTF-8''E2E%20%E5%AE%A2%E6%88%B7%E7%AB%AF.yaml`) {
		t.Fatalf("Mihomo subscription filename is not UTF-8 encoded: %q", disposition)
	}
	for _, expected := range []string{"store-selected: true", "tun:", "strict-route: true", "proxies:", `"server":"e2e.example.test"`, "proxy-groups:", `"name":"全部节点"`, "rule-providers:", `"url":"https://rules.example.test/proxy.mrs"`, "rules:", "RULE-SET,远程代理规则,全部节点", "GEOSITE,CN,DIRECT", "enhanced-mode: fake-ip", "nameserver:\n    - rcode://success", "direct-nameserver:", "https://223.5.5.5/dns-query"} {
		if !strings.Contains(string(yaml), expected) {
			t.Fatalf("Mihomo subscription is missing %q:\n%s", expected, yaml)
		}
	}

	var tasksPage struct {
		Total int            `json:"total"`
		Tasks []taskResponse `json:"tasks"`
	}
	api.mustJSON(t, http.MethodGet, "/api/v1/tasks?page=1&page_size=1", nil, false, http.StatusOK, &tasksPage)
	if tasksPage.Total < 2 || len(tasksPage.Tasks) != 1 {
		t.Fatalf("task pagination is not effective: %#v", tasksPage)
	}
	var auditPage struct {
		Total  int               `json:"total"`
		Events []json.RawMessage `json:"audit_events"`
	}
	api.mustJSON(t, http.MethodGet, "/api/v1/audit-events?page=1&page_size=1", nil, true, http.StatusOK, &auditPage)
	if auditPage.Total == 0 || len(auditPage.Events) != 1 {
		t.Fatalf("audit pagination is not effective: %#v", auditPage)
	}

	api.mustJSON(t, http.MethodPost, "/api/v1/auth/logout", map[string]any{}, true, http.StatusNoContent, nil)
	status, _, _ = rawRequest(t, api.client, http.MethodGet, baseURL+"/api/v1/auth/me", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("session remained valid after logout: HTTP %d", status)
	}
}

type taskResponse struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	ResultSummary string `json:"result_summary"`
}

type quickListenerResponse struct {
	Listener struct {
		ID string `json:"id"`
	} `json:"listener"`
	Endpoints []struct {
		ID string `json:"id"`
	} `json:"endpoints"`
}

type apiClient struct {
	baseURL string
	client  *http.Client
	csrf    string
}

func newAPIClient(t *testing.T, baseURL string) *apiClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &apiClient{baseURL: baseURL, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (a *apiClient) login(t *testing.T, secret string) {
	t.Helper()
	var challenge struct {
		ID string `json:"challenge_id"`
	}
	a.mustJSON(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": e2eAdminEmail, "password": e2eAdminPassword}, false, http.StatusOK, &challenge)
	var session struct {
		CSRF string `json:"csrf_token"`
		Role string `json:"role"`
	}
	a.mustJSON(t, http.MethodPost, "/api/v1/auth/mfa", map[string]string{"challenge_id": challenge.ID, "code": currentTOTP(t, secret)}, false, http.StatusOK, &session)
	if session.Role != "admin" || session.CSRF == "" {
		t.Fatalf("unexpected authenticated session: %#v", session)
	}
	a.csrf = session.CSRF
}

func (a *apiClient) mustJSON(t *testing.T, method, path string, payload any, csrf bool, expected int, target any) http.Header {
	t.Helper()
	status, header, body := rawRequest(t, a.client, method, a.baseURL+path, payload, conditional(csrf, a.csrf))
	if status != expected {
		t.Fatalf("%s %s returned HTTP %d, want %d: %s", method, path, status, expected, body)
	}
	if target != nil && len(body) > 0 {
		if err := json.Unmarshal(body, target); err != nil {
			t.Fatalf("decode %s %s response: %v\n%s", method, path, err, body)
		}
	}
	return header
}

func (a *apiClient) tryJSON(method, path string, payload any, csrf bool, expected int, target any) bool {
	status, _, body := rawRequestNoFail(a.client, method, a.baseURL+path, payload, conditional(csrf, a.csrf))
	return status == expected && json.Unmarshal(body, target) == nil
}

func rawRequest(t *testing.T, client *http.Client, method, url string, payload any, csrf string) (int, http.Header, []byte) {
	t.Helper()
	status, header, body := rawRequestNoFail(client, method, url, payload, csrf)
	if status == 0 {
		t.Fatalf("%s %s failed: %s", method, url, body)
	}
	return status, header, body
}

func rawRequestNoFail(client *http.Client, method, url string, payload any, csrf string) (int, http.Header, []byte) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, []byte(err.Error())
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		return 0, nil, []byte(err.Error())
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, []byte(err.Error())
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
	if err != nil {
		return 0, response.Header.Clone(), []byte(err.Error())
	}
	return response.StatusCode, response.Header.Clone(), content
}

func waitForTask(t *testing.T, api *apiClient, id string, timeout time.Duration) taskResponse {
	t.Helper()
	var task taskResponse
	waitFor(t, timeout, "task "+id+" to reach a terminal state", func() bool {
		if !api.tryJSON(http.MethodGet, "/api/v1/tasks/"+id, nil, false, http.StatusOK, &task) {
			return false
		}
		return task.Status == "succeeded" || task.Status == "failed" || task.Status == "rolled_back"
	})
	return task
}

func waitForSuccessfulTaskHeader(t *testing.T, api *apiClient, headers http.Header, name string) {
	t.Helper()
	id := headers.Get(name)
	if id == "" {
		t.Fatalf("response is missing %s", name)
	}
	completed := waitForTask(t, api, id, 20*time.Second)
	if completed.Status != "succeeded" {
		t.Fatalf("task from %s did not succeed: %#v", name, completed)
	}
}

func startClashAPI(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connections" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"connections":[{"id":"e2e-connection","metadata":{"network":"tcp","type":"vless","sourceIP":"192.168.1.10","sourcePort":"42000","destinationIP":"198.51.100.20","destinationPort":"443","host":"example.test"},"upload":128,"download":512,"start":"2026-08-02T00:00:00Z","chains":["direct"],"rule":"DOMAIN","rulePayload":"example.test"}]}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func selfSignedCertificate(t *testing.T, names []string) (string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return string(certificatePEM), string(privateKeyPEM)
}

func verifyEmbeddedWebApplication(t *testing.T, baseURL string) {
	t.Helper()
	status, _, index := rawRequest(t, http.DefaultClient, http.MethodGet, baseURL+"/", nil, "")
	if status != http.StatusOK || !bytes.Contains(index, []byte("sb-control")) {
		t.Fatalf("embedded web application is unavailable: HTTP %d", status)
	}
	assetMatch := regexp.MustCompile(`(?:src|href)="(/[^"]+\.(?:js|css))"`).FindSubmatch(index)
	if len(assetMatch) != 2 {
		t.Fatalf("embedded index does not reference a built asset:\n%s", index)
	}
	status, _, asset := rawRequest(t, http.DefaultClient, http.MethodGet, baseURL+string(assetMatch[1]), nil, "")
	if status != http.StatusOK || len(asset) < 100 {
		t.Fatalf("embedded asset %s is unavailable: HTTP %d, bytes=%d", assetMatch[1], status, len(asset))
	}
}

func verifyUnauthenticatedRequestIsRejected(t *testing.T, baseURL string) {
	t.Helper()
	status, _, _ := rawRequest(t, http.DefaultClient, http.MethodGet, baseURL+"/api/v1/nodes", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated node query returned HTTP %d", status)
	}
}

func assertAuthenticatedUserRoute(t *testing.T, content, user, outbound string) {
	t.Helper()
	var configuration struct {
		Route struct {
			Rules []struct {
				AuthUser []string `json:"auth_user"`
				Outbound string   `json:"outbound"`
			} `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(content), &configuration); err != nil {
		t.Fatalf("decode applied sing-box configuration: %v", err)
	}
	for _, rule := range configuration.Route.Rules {
		if len(rule.AuthUser) == 1 && rule.AuthUser[0] == user && rule.Outbound == outbound {
			return
		}
	}
	t.Fatalf("applied configuration does not route user %q through %q:\n%s", user, outbound, content)
}

func registrationExists(registrations []struct {
	ID string `json:"id"`
}, id string) bool {
	for _, registration := range registrations {
		if registration.ID == id {
			return true
		}
	}
	return false
}

func initializeAdministrator(t *testing.T, binaryPath, dataDir string) string {
	t.Helper()
	command := exec.Command(binaryPath, "master", "init-admin", "--data-dir", dataDir, "--email", e2eAdminEmail, "--password-stdin")
	command.Stdin = strings.NewReader(e2eAdminPassword + "\n")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("initialize administrator: %v\n%s", err, output)
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		t.Fatal("administrator initialization did not return a TOTP secret")
	}
	return fields[len(fields)-1]
}

func currentTOTP(t *testing.T, secret string) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		t.Fatal(err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(time.Now().Unix()/30))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := (uint32(digest[offset])&0x7f)<<24 | uint32(digest[offset+1])<<16 | uint32(digest[offset+2])<<8 | uint32(digest[offset+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate E2E source file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), ".."))
}

func buildProgram(t *testing.T, root, packagePath, output string) string {
	t.Helper()
	goBinary := filepath.Join(runtime.GOROOT(), "bin", executableName("go"))
	command := exec.Command(goBinary, "build", "-trimpath", "-o", output, packagePath)
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if built, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", packagePath, err, built)
	}
	return output
}

func commandLastField(t *testing.T, binaryPath string, args ...string) string {
	t.Helper()
	output := runCommand(t, 15*time.Second, nil, binaryPath, args...)
	fields := strings.Fields(output)
	if len(fields) == 0 {
		t.Fatalf("command returned no value: %s %s", binaryPath, strings.Join(args, " "))
	}
	return fields[len(fields)-1]
}

func runCommand(t *testing.T, timeout time.Duration, environment []string, binaryPath string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, binaryPath, args...)
	if environment != nil {
		command.Env = environment
	}
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("command timed out: %s %s", binaryPath, strings.Join(args, " "))
	}
	if err != nil {
		t.Fatalf("command failed: %s %s: %v\n%s", binaryPath, strings.Join(args, " "), err, output)
	}
	return string(output)
}

type runningProcess struct {
	name    string
	command *exec.Cmd
	logPath string
	logFile *os.File
}

func startProcess(t *testing.T, name, binaryPath string, environment []string, args ...string) *runningProcess {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), name+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binaryPath, args...)
	if environment != nil {
		command.Env = environment
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		logFile.Close()
		t.Fatalf("start %s: %v", name, err)
	}
	return &runningProcess{name: name, command: command, logPath: logPath, logFile: logFile}
}

func (p *runningProcess) stop(t *testing.T) {
	t.Helper()
	if p.command.Process != nil {
		_ = p.command.Process.Kill()
		_ = p.command.Wait()
	}
	_ = p.logFile.Close()
	if t.Failed() {
		if content, err := os.ReadFile(p.logPath); err == nil && len(content) > 0 {
			t.Logf("%s process log:\n%s", p.name, content)
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func rejectingTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func copyExecutable(t *testing.T, source, destination string) {
	t.Helper()
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, content, 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		t.Fatalf("expected file %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func conditional(condition bool, value string) string {
	if condition {
		return value
	}
	return ""
}
