package control

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type RealityCandidateReport struct {
	Domain         string   `json:"domain"`
	Target         string   `json:"target"`
	IP             string   `json:"ip"`
	Port           uint16   `json:"port"`
	TLS13          bool     `json:"tls13"`
	TLS_13         bool     `json:"tls_13"`
	ALPN           []string `json:"alpn"`
	LatencyMs      int      `json:"latency_ms"`
	Issuer         string   `json:"issuer"`
	CertIssuer     string   `json:"cert_issuer"`
	Subject        string   `json:"subject"`
	ExpiresAt      string   `json:"expires_at"`
	CertExpiresAt  string   `json:"cert_expires_at"`
	CertDaysLeft   int      `json:"cert_days_left"`
	Compatible     bool     `json:"compatible"`
	Verdict        string   `json:"verdict"`
	Recommendation string   `json:"recommendation"`
	Warnings       []string `json:"warnings"`
}

// CheckRealityTarget tests whether a target server is a high-grade disguise candidate for VLESS Reality.
func CheckRealityTarget(ctx context.Context, domain string, port uint16) (RealityCandidateReport, error) {
	domain = strings.TrimSpace(strings.TrimSuffix(domain, "."))
	if domain == "" {
		return RealityCandidateReport{}, errors.New("域名不能为空")
	}
	if port == 0 {
		port = 443
	}

	report := RealityCandidateReport{
		Domain:   domain,
		Port:     port,
		Warnings: []string{},
	}

	// 1. Resolve domain
	dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	ips, err := net.DefaultResolver.LookupIP(dialCtx, "ip", domain)
	if err != nil || len(ips) == 0 {
		return RealityCandidateReport{}, fmt.Errorf("解析域名 %s 失败: %w", domain, err)
	}
	report.IP = ips[0].String()

	address := net.JoinHostPort(domain, strconv.Itoa(int(port)))

	// 2. Dial TLS
	started := time.Now()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	rawConn, err := dialer.DialContext(dialCtx, "tcp", address)
	if err != nil {
		return RealityCandidateReport{}, fmt.Errorf("连接目标主机 %s 失败: %w", address, err)
	}
	defer rawConn.Close()

	tlsConfig := &tls.Config{
		ServerName:         domain,
		NextProtos:         []string{"h2", "http/1.1"},
		InsecureSkipVerify: true, // We inspect certs manually
	}

	tlsConn := tls.Client(rawConn, tlsConfig)
	if err := tlsConn.HandshakeContext(dialCtx); err != nil {
		return RealityCandidateReport{}, fmt.Errorf("TLS 握手失败: %w", err)
	}
	defer tlsConn.Close()

	report.LatencyMs = int(time.Since(started).Milliseconds())
	state := tlsConn.ConnectionState()

	// 3. Check TLS Version
	if state.Version == tls.VersionTLS13 {
		report.TLS13 = true
	} else {
		report.TLS13 = false
		report.Warnings = append(report.Warnings, "目标站点不支持 TLS 1.3，不推荐作为 Reality 伪装目标")
	}

	// 4. Check ALPN
	if state.NegotiatedProtocol != "" {
		report.ALPN = []string{state.NegotiatedProtocol}
	} else {
		report.Warnings = append(report.Warnings, "目标未协商出 ALPN 协议")
	}

	report.Target = domain
	report.TLS_13 = report.TLS13

	// 5. Inspect Certificates
	if len(state.PeerCertificates) > 0 {
		cert := state.PeerCertificates[0]
		report.Issuer = cert.Issuer.CommonName
		if report.Issuer == "" && len(cert.Issuer.Organization) > 0 {
			report.Issuer = cert.Issuer.Organization[0]
		}
		report.CertIssuer = report.Issuer
		report.Subject = cert.Subject.CommonName
		report.ExpiresAt = cert.NotAfter.UTC().Format(time.RFC3339)
		report.CertExpiresAt = report.ExpiresAt
		daysLeft := int(time.Until(cert.NotAfter).Hours() / 24)
		if daysLeft < 0 {
			daysLeft = 0
		}
		report.CertDaysLeft = daysLeft

		if time.Now().After(cert.NotAfter) {
			report.Warnings = append(report.Warnings, "目标站点证书已过期")
		}
	}

	// 6. Final verdict
	if report.TLS13 && (state.NegotiatedProtocol == "h2" || state.NegotiatedProtocol == "http/1.1") {
		report.Compatible = true
		report.Verdict = "优质伪装目标：完美支持 TLS 1.3 与 HTTP/2"
	} else if report.TLS13 {
		report.Compatible = true
		report.Verdict = "可用伪装目标：支持 TLS 1.3"
	} else {
		report.Compatible = false
		report.Verdict = "高风险目标：不支持 TLS 1.3，易被识别"
	}
	report.Recommendation = report.Verdict

	return report, nil
}
