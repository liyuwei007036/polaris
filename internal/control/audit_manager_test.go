package control_test

import (
	"testing"

	"github.com/liyuwei007036/polaris/internal/control"
)

func TestAuditManagerTranslationAndDiagnostics(t *testing.T) {
	// 1. Test TranslateAuditSummary
	summary1 := control.TranslateAuditSummary("listener.created", "listener", "东京 VLESS", "listener definition created")
	if summary1 != "创建接入服务「东京 VLESS」" {
		t.Fatalf("unexpected summary: %s", summary1)
	}

	summary2 := control.TranslateAuditSummary("endpoint.created", "endpoint", "MacBook", "endpoint credentials created")
	if summary2 != "为节点「MacBook」签发接入凭证与连接密钥" {
		t.Fatalf("unexpected summary: %s", summary2)
	}

	summary3 := control.TranslateAuditSummary("task.completed.succeeded", "task", "", "agent task completed: singbox.apply_config")
	if summary3 != "服务器完成系统任务：更新连接配置" {
		t.Fatalf("unexpected summary: %s", summary3)
	}

	// 2. Test FormatTaskDiagnostic
	diagSuccess := control.FormatTaskDiagnostic("succeeded", "applied")
	if diagSuccess != "配置已生效" {
		t.Fatalf("unexpected diagnostic: %s", diagSuccess)
	}

	diagPortConflict := control.FormatTaskDiagnostic("failed", "listen tcp 0.0.0.0:443: bind: address already in use")
	if diagPortConflict == "" || !containsSubstr(diagPortConflict, "端口已被占用") {
		t.Fatalf("expected port conflict diagnosis, got: %s", diagPortConflict)
	}

	diagCert := control.FormatTaskDiagnostic("failed", "tls: failed to parse private key")
	if diagCert == "" || !containsSubstr(diagCert, "证书") {
		t.Fatalf("expected certificate diagnosis, got: %s", diagCert)
	}

	diagRollback := control.FormatTaskDiagnostic("rolled_back", "sing-box service failed to start")
	if diagRollback == "" || !containsSubstr(diagRollback, "已恢复原配置") {
		t.Fatalf("expected rollback diagnosis, got: %s", diagRollback)
	}
}

func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) > 0 && searchSubstr(s, sub))
}

func searchSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (stringIndex(s, sub) >= 0)
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestAuditManagerStoreFilteringAndStats(t *testing.T) {
	store, _, _, _, _ := newFeatureServer(t)

	operators, err := store.ListOperators(t.Context())
	if err != nil || len(operators) == 0 {
		t.Fatalf("list operators: %v", err)
	}
	opID := operators[0].ID

	// Insert structured audit records
	err = store.AppendAuditRecord(t.Context(), control.AuditRecord{
		OperatorID: opID,
		Action:     "listener.created",
		TargetType: "listener",
		TargetID:   "lis-test-01",
		TargetName: "东京 VLESS Reality",
		Summary:    "创建接入服务「东京 VLESS Reality」",
		Detail:     `{"port": 443, "protocol": "vless"}`,
		IPAddress:  "192.168.1.100",
		Status:     "succeeded",
	})
	if err != nil {
		t.Fatalf("append audit: %v", err)
	}

	err = store.AppendAuditRecord(t.Context(), control.AuditRecord{
		OperatorID: opID,
		Action:     "firewall.rule_add",
		TargetType: "firewall_rule",
		TargetID:   "fw-test-01",
		TargetName: "放行 8443",
		Summary:    "添加防火墙放行规则 8443/tcp",
		Detail:     `{"port": 8443, "proto": "tcp"}`,
		IPAddress:  "192.168.1.100",
		Status:     "succeeded",
	})
	if err != nil {
		t.Fatalf("append audit: %v", err)
	}

	// Filter by targetType
	events, total, err := store.ListAuditFiltered(t.Context(), "", "listener", "", 1, 10)
	if err != nil || total < 1 || len(events) < 1 {
		t.Fatalf("expected at least 1 listener event, got total=%d, err=%v", total, err)
	}
	if events[0].TargetName != "东京 VLESS Reality" {
		t.Fatalf("expected target name '东京 VLESS Reality', got '%s'", events[0].TargetName)
	}

	// Filter by keyword
	events, total, err = store.ListAuditFiltered(t.Context(), "8443", "", "", 1, 10)
	if err != nil || total != 1 || len(events) != 1 {
		t.Fatalf("expected 1 match for keyword '8443', got total=%d, err=%v", total, err)
	}

	// Stats
	stats, err := store.GetAuditStats(t.Context())
	if err != nil {
		t.Fatalf("get audit stats: %v", err)
	}
	if stats.TodayAuditCount < 2 {
		t.Fatalf("expected at least 2 audits today, got %d", stats.TodayAuditCount)
	}
}
