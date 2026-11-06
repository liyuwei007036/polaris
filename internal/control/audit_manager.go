package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AuditStats represents a high-level summary of recent operation activity.
type AuditStats struct {
	TodayAuditCount int `json:"today_audit_count"`
	TodayTaskCount  int `json:"today_task_count"`
	FailedCount     int `json:"failed_count"`
	ActiveOperators int `json:"active_operators"`
}

// AuditRecord contains full structured data for logging an operator event.
type AuditRecord struct {
	OperatorID string
	Action     string
	TargetType string
	TargetID   string
	TargetName string
	Summary    string
	Detail     string
	IPAddress  string
	Status     string
}

// AppendAuditRecord stores a fully structured audit event.
func (s *Store) AppendAuditRecord(ctx context.Context, r AuditRecord) error {
	id, err := newID()
	if err != nil {
		return err
	}
	if r.Status == "" {
		r.Status = "succeeded"
	}
	if r.Detail == "" {
		r.Detail = "{}"
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_events (id, operator_id, action, target_type, target_id, target_name, summary, detail, ip_address, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, r.OperatorID, r.Action, r.TargetType, r.TargetID, r.TargetName, r.Summary, r.Detail, r.IPAddress, r.Status, nowUnix())
	if err != nil {
		return fmt.Errorf("append audit record: %w", err)
	}
	return nil
}

// TranslateTaskKind returns an easy-to-understand Chinese label for a task kind.
func TranslateTaskKind(kind string) string {
	kinds := map[string]string{
		"singbox.apply_config": "更新连接配置",
		"singbox.install":      "安装服务核心",
		"singbox.upgrade":      "升级服务核心",
		"dns.update":           "刷新域名解析",
		"firewall.query":       "读取防火墙",
		"firewall.mutate":      "同步防火墙策略",
		"fail2ban.query":       "读取封禁设置",
		"fail2ban.mutate":      "修改封禁策略",
		"fail2ban.unban":       "解封 IP 地址",
		"nginx.apply_config":   "更新端口分流",
		"outbound.test":        "检测出口连通",
		"agent.upgrade":        "升级 Agent 探针",
	}
	if name, ok := kinds[kind]; ok {
		return name
	}
	return "系统操作 (" + kind + ")"
}

// FormatTaskDiagnostic translates technical logs and systemd errors into human-friendly Chinese diagnosis.
func FormatTaskDiagnostic(status, rawSummary string) string {
	raw := strings.TrimSpace(rawSummary)
	if status == "succeeded" {
		if strings.Contains(raw, "连接服务配置编译完成") || strings.Contains(raw, "顺利平滑重载") {
			return "配置已生效"
		}
		if strings.Contains(raw, "DDNS 动态域名解析刷新完成") {
			return "解析已刷新"
		}
		if strings.Contains(raw, "SNI 分流接入端口监听分配成功") {
			return "端口分配成功"
		}
		if strings.Contains(raw, "防火墙安全策略放行") {
			return "防火墙已同步"
		}
		if raw != "" && containsChinese(raw) {
			return raw
		}
		return "配置已生效"
	}

	lower := strings.ToLower(raw)
	var cause string
	switch {
	case strings.Contains(lower, "address already in use") || strings.Contains(lower, "bind: address"):
		cause = "端口已被占用，请排查端口冲突"
	case strings.Contains(lower, "certificate") || strings.Contains(lower, "x509") || strings.Contains(lower, "tls:") || strings.Contains(lower, "private key"):
		cause = "TLS 证书或私钥无效"
	case strings.Contains(lower, "job for sing-box.service failed") || strings.Contains(lower, "control process exited"):
		cause = "服务启动失败，进程异常退出"
	case strings.Contains(lower, "failed to parse") || strings.Contains(lower, "syntax error") || strings.Contains(lower, "unknown field"):
		cause = "配置解析失败，参数格式不规范"
	case strings.Contains(lower, "deadline exceeded") || strings.Contains(lower, "timeout") || strings.Contains(lower, "connection refused"):
		cause = "节点通信超时，未能收到确认结果"
	case strings.Contains(lower, "permission denied"):
		cause = "系统权限不足，命令执行被拒绝"
	case strings.Contains(raw, "连接中断") || strings.Contains(raw, "任务已终止"):
		cause = "下发过程中节点连接中断"
	default:
		if raw != "" && containsChinese(raw) {
			cause = raw
		} else if raw != "" {
			cause = raw
		} else {
			cause = "操作未完成"
		}
	}

	if status == "rolled_back" {
		return cause + "，已恢复原配置"
	}
	return cause
}

// TranslateAuditSummary converts technical and legacy English audit summaries into intuitive Chinese.
func TranslateAuditSummary(action, targetType, targetName, oldSummary string) string {
	trimmed := strings.TrimSpace(oldSummary)
	if trimmed != "" && containsChinese(trimmed) {
		return trimmed
	}

	name := targetName
	if name == "" {
		name = "对应对象"
	}

	lower := strings.ToLower(trimmed)
	switch {
	case lower == "listener and generated accounts created":
		return fmt.Sprintf("创建接入服务「%s」并自动生成默认接入用户", name)
	case lower == "listener definition created":
		return fmt.Sprintf("创建接入服务「%s」", name)
	case lower == "listener definition updated":
		return fmt.Sprintf("修改接入服务「%s」的配置参数", name)
	case lower == "listener enabled state changed":
		return fmt.Sprintf("切换接入服务「%s」的启用状态", name)
	case lower == "listener and endpoints deleted":
		return fmt.Sprintf("删除接入服务「%s」及其关联的所有用户凭据", name)
	case lower == "endpoint credentials created" || lower == "generated endpoint credentials created":
		return fmt.Sprintf("为用户「%s」签发接入凭证与连接密钥", name)
	case lower == "endpoint definition updated":
		return fmt.Sprintf("更新用户「%s」的配置与出站路由出口", name)
	case lower == "endpoint enabled state changed":
		return fmt.Sprintf("切换用户「%s」的启用状态", name)
	case lower == "endpoint credentials deleted":
		return fmt.Sprintf("删除用户「%s」的接入凭据", name)
	case lower == "node name, client address, and ddns settings updated" || lower == "node updated":
		return fmt.Sprintf("更新服务器「%s」的基础信息与动态解析设置", name)
	case lower == "client connection address updated":
		return fmt.Sprintf("更新服务器「%s」的客户端连接公网地址", name)
	case lower == "node name updated":
		return fmt.Sprintf("修改服务器名称为「%s」", name)
	case lower == "node public key approved":
		return fmt.Sprintf("批准新服务器「%s」加入集群并建立双向安全加密隧道", name)
	case lower == "node revoked":
		return fmt.Sprintf("吊销服务器「%s」的集群访问授权", name)
	case lower == "compiled configuration task requested":
		return fmt.Sprintf("下发并应用最新连接服务配置到服务器「%s」", name)
	case lower == "compiled nginx stream task requested":
		return fmt.Sprintf("下发并应用 SNI 端口分流配置到服务器「%s」", name)
	case lower == "outbound definition created":
		return fmt.Sprintf("创建上网出口「%s」", name)
	case lower == "outbound definition updated":
		return fmt.Sprintf("修改上网出口「%s」的连接配置", name)
	case lower == "outbound enabled state changed":
		return fmt.Sprintf("切换上网出口「%s」启用状态", name)
	case lower == "outbound deleted":
		return fmt.Sprintf("删除上网出口「%s」", name)
	case lower == "outbound connectivity test requested":
		return fmt.Sprintf("发起对上网出口「%s」的网络连通性测速", name)
	case lower == "structured route rule created":
		return "创建新的服务器分流规则"
	case lower == "structured route rule updated":
		return "更新服务器分流规则配置"
	case lower == "route rule enabled state changed":
		return "切换服务器分流规则启用状态"
	case lower == "route rule priority changed":
		return "调整服务器分流规则的优先级顺序"
	case lower == "route rule deleted":
		return "删除服务器分流规则"
	case lower == "server firewall rule changed":
		return "修改服务器防火墙安全访问策略"
	case lower == "server fail2ban jail changed":
		return "更新 Fail2ban 自动防爆破封禁规则"
	case lower == "banned address released":
		return "手动解封被封禁的客户端 IP 地址"
	case lower == "operator changed own password":
		return "管理员修改个人登录密码"
	case lower == "operator password reset":
		return fmt.Sprintf("重置管理员「%s」的登录密码", name)
	case lower == "operator enabled two-factor authentication":
		return "启用管理员两步验证 (2FA / TOTP)"
	case lower == "operator disabled two-factor authentication":
		return "关闭管理员两步验证 (2FA / TOTP)"
	case lower == "operator two-factor authentication disabled":
		return fmt.Sprintf("重置管理员「%s」的两步验证设置", name)
	case lower == "operator created; mfa secret omitted":
		return fmt.Sprintf("创建新管理员账户「%s」", name)
	case lower == "operator role or enabled state updated":
		return fmt.Sprintf("更新管理员「%s」的角色权限与状态", name)
	case lower == "managed reality key created; private key omitted":
		return fmt.Sprintf("生成新的 Reality 安全密钥对「%s」", name)
	case lower == "lifetime recorded; token omitted":
		return "生成服务器节点接入注册令牌"
	case lower == "ip location database reloaded":
		return "重新加载本地 IP 归属地高精度数据库"
	case lower == "high precision mmdb downloaded and activated":
		return "自动下载并激活最新高精度 MMDB 离线 IP 库"
	case strings.HasPrefix(lower, "agent task completed: "):
		taskKind := strings.TrimPrefix(lower, "agent task completed: ")
		return fmt.Sprintf("节点完成系统任务：%s", TranslateTaskKind(taskKind))
	case lower == "mihomo proxy group created":
		return fmt.Sprintf("创建 Mihomo 代理策略分组「%s」", name)
	case lower == "mihomo proxy group updated":
		return fmt.Sprintf("更新 Mihomo 代理策略分组「%s」", name)
	case lower == "mihomo proxy group deleted":
		return fmt.Sprintf("删除 Mihomo 代理策略分组「%s」", name)
	case lower == "mihomo routing profile created":
		return fmt.Sprintf("创建 Mihomo 客户端分流规则方案「%s」", name)
	case lower == "mihomo routing profile updated":
		return fmt.Sprintf("更新 Mihomo 客户端分流规则方案「%s」", name)
	case lower == "mihomo routing profile deleted":
		return fmt.Sprintf("删除 Mihomo 客户端分流规则方案「%s」", name)
	case lower == "mihomo client config created":
		return fmt.Sprintf("创建客户端通用订阅配置「%s」", name)
	case lower == "mihomo client config copied":
		return fmt.Sprintf("复制生成客户端通用订阅配置「%s」", name)
	case lower == "mihomo client config updated":
		return fmt.Sprintf("更新客户端通用订阅配置「%s」", name)
	case lower == "mihomo client config deleted":
		return fmt.Sprintf("删除客户端通用订阅配置「%s」", name)
	case lower == "mihomo rule provider created":
		return fmt.Sprintf("添加 Mihomo 规则供应商「%s」", name)
	case lower == "mihomo rule provider updated":
		return fmt.Sprintf("更新 Mihomo 规则供应商「%s」", name)
	case lower == "mihomo rule provider deleted":
		return fmt.Sprintf("删除 Mihomo 规则供应商「%s」", name)
	case lower == "subscription created; url and access token omitted":
		return fmt.Sprintf("创建客户端更新订阅「%s」", name)
	case lower == "subscription updated; url and access token omitted":
		return fmt.Sprintf("修改客户端更新订阅「%s」", name)
	case lower == "client subscription access token rotated; token omitted":
		return fmt.Sprintf("轮换客户端更新订阅「%s」的访问令牌", name)
	case strings.HasPrefix(lower, "read-only share link created"):
		return "生成只读分享链接"
	case lower == "read-only share link revoked":
		return "撤销只读分享链接"
	case strings.HasPrefix(lower, "signed agent upgrade task requested"):
		return fmt.Sprintf("为服务器「%s」下发 Agent 探针升级任务", name)
	case strings.HasPrefix(lower, "signed sing-box installation task requested"):
		return fmt.Sprintf("为服务器「%s」下发 sing-box 核心安装任务", name)
	}

	// Fallback based on action
	actionParts := strings.Split(action, ".")
	lastPart := actionParts[len(actionParts)-1]
	actionChinese := map[string]string{
		"created":        "创建",
		"updated":        "修改",
		"deleted":        "删除",
		"state_changed":  "状态变更",
		"rule_add":       "添加规则",
		"rule_delete":    "删除规则",
		"jail_save":      "保存规则",
		"jail_delete":    "删除规则",
		"unbanned":       "解封 IP",
		"settings_updated": "更新设置",
		"settings_deleted": "删除设置",
	}

	targetChinese := map[string]string{
		"listener":              "接入服务",
		"node":                  "服务器节点",
		"endpoint":              "接入用户",
		"outbound":              "上网出口",
		"route_rule":            "分流规则",
		"operator":              "管理人员",
		"cloudflare":            "域名解析设置",
		"cloudflare_record":     "域名解析记录",
		"origin_certificate":    "TLS 原点证书",
		"reality_key":           "Reality 密钥",
		"subscription":          "客户端订阅",
		"mihomo_proxy_group":    "代理分组",
		"mihomo_routing_profile": "分流方案",
		"mihomo_client_config":  "客户端配置",
		"mihomo_rule_provider":  "规则供应商",
		"share":                 "只读分享",
		"system":                "系统设置",
	}

	verb := actionChinese[lastPart]
	target := targetChinese[targetType]
	if verb != "" && target != "" {
		return fmt.Sprintf("%s%s「%s」", verb, target, name)
	}
	if verb != "" {
		return fmt.Sprintf("%s「%s」", verb, name)
	}
	return fmt.Sprintf("变更 %s「%s」", targetType, name)
}

// resolveTargetName dynamically looks up human-readable names for targets that had none recorded.
func (s *Store) resolveTargetName(ctx context.Context, targetType, targetID string) string {
	if targetID == "" {
		return ""
	}
	var name string
	var err error
	switch targetType {
	case "node":
		err = s.db.QueryRowContext(ctx, `SELECT name FROM nodes WHERE id = ?`, targetID).Scan(&name)
	case "listener":
		err = s.db.QueryRowContext(ctx, `SELECT name FROM listeners WHERE id = ?`, targetID).Scan(&name)
	case "endpoint":
		err = s.db.QueryRowContext(ctx, `SELECT CASE WHEN alias != '' THEN alias ELSE name END FROM endpoints WHERE id = ?`, targetID).Scan(&name)
	case "outbound":
		err = s.db.QueryRowContext(ctx, `SELECT name FROM outbounds WHERE id = ?`, targetID).Scan(&name)
	case "operator":
		err = s.db.QueryRowContext(ctx, `SELECT email FROM operators WHERE id = ?`, targetID).Scan(&name)
	case "subscription":
		err = s.db.QueryRowContext(ctx, `SELECT name FROM subscriptions WHERE id = ?`, targetID).Scan(&name)
	case "mihomo_proxy_group":
		err = s.db.QueryRowContext(ctx, `SELECT name FROM mihomo_proxy_groups WHERE id = ?`, targetID).Scan(&name)
	case "mihomo_routing_profile":
		err = s.db.QueryRowContext(ctx, `SELECT name FROM mihomo_routing_profiles WHERE id = ?`, targetID).Scan(&name)
	case "mihomo_client_config":
		err = s.db.QueryRowContext(ctx, `SELECT name FROM mihomo_client_configs WHERE id = ?`, targetID).Scan(&name)
	case "mihomo_rule_provider":
		err = s.db.QueryRowContext(ctx, `SELECT name FROM mihomo_rule_providers WHERE id = ?`, targetID).Scan(&name)
	case "managed_reality_key", "reality_key":
		err = s.db.QueryRowContext(ctx, `SELECT name FROM managed_reality_keys WHERE id = ?`, targetID).Scan(&name)
	case "origin_certificate":
		err = s.db.QueryRowContext(ctx, `SELECT domain FROM origin_certificates WHERE id = ?`, targetID).Scan(&name)
	case "route_rule":
		var inTag, outTag string
		if err2 := s.db.QueryRowContext(ctx, `SELECT inbound_tag, outbound_tag FROM route_rules WHERE id = ?`, targetID).Scan(&inTag, &outTag); err2 == nil {
			name = fmt.Sprintf("%s -> %s", inTag, outTag)
		}
	}
	if err == nil && name != "" {
		return name
	}
	return ""
}

// ListAuditFiltered returns paginated audit records with advanced server-side search and filtering.
func (s *Store) ListAuditFiltered(ctx context.Context, keyword, targetType, status string, page, pageSize int) ([]AuditEvent, int, error) {
	where := " WHERE 1 = 1"
	var args []any

	if targetType != "" {
		// Category matching support
		switch targetType {
		case "security":
			where += " AND (a.target_type IN ('firewall_rule', 'fail2ban_jail') OR a.action LIKE 'firewall.%' OR a.action LIKE 'fail2ban.%')"
		case "mihomo":
			where += " AND (a.target_type LIKE 'mihomo_%' OR a.target_type = 'subscription' OR a.action LIKE 'mihomo.%')"
		case "cloudflare":
			where += " AND (a.target_type IN ('cloudflare', 'cloudflare_record', 'origin_certificate') OR a.action LIKE 'cloudflare%')"
		default:
			where += " AND a.target_type = ?"
			args = append(args, targetType)
		}
	}

	if status != "" {
		where += " AND a.status = ?"
		args = append(args, status)
	}

	if keyword != "" {
		pattern := "%" + strings.TrimSpace(keyword) + "%"
		where += " AND (a.summary LIKE ? OR a.target_name LIKE ? OR a.action LIKE ? OR o.email LIKE ? OR a.ip_address LIKE ? OR a.target_id LIKE ?)"
		args = append(args, pattern, pattern, pattern, pattern, pattern, pattern)
	}

	countQuery := `SELECT COUNT(*) FROM audit_events a LEFT JOIN operators o ON o.id = a.operator_id` + where
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count filtered audit events: %w", err)
	}

	query := `SELECT a.id, a.operator_id, COALESCE(o.email, '系统/未知'), a.action, a.target_type, a.target_id,
		COALESCE(a.target_name, ''), a.summary, COALESCE(a.detail, ''), COALESCE(a.ip_address, ''),
		COALESCE(a.status, 'succeeded'), a.created_at
		FROM audit_events a
		LEFT JOIN operators o ON o.id = a.operator_id` + where + ` ORDER BY a.created_at DESC, a.id DESC LIMIT ? OFFSET ?`

	pagedArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, query, pagedArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list filtered audit events: %w", err)
	}
	defer rows.Close()

	var events []AuditEvent
	for rows.Next() {
		var event AuditEvent
		var createdAt int64
		if err := rows.Scan(&event.ID, &event.OperatorID, &event.Username, &event.Action, &event.TargetType, &event.TargetID,
			&event.TargetName, &event.Summary, &event.Detail, &event.IPAddress, &event.Status, &createdAt); err != nil {
			return nil, 0, fmt.Errorf("scan audit event: %w", err)
		}
		event.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		events = append(events, event)
	}
	_ = rows.Close()

	for i := range events {
		if events[i].TargetName == "" {
			events[i].TargetName = s.resolveTargetName(ctx, events[i].TargetType, events[i].TargetID)
		}
		events[i].Summary = TranslateAuditSummary(events[i].Action, events[i].TargetType, events[i].TargetName, events[i].Summary)
	}

	return events, total, nil
}

// ListTasksFiltered returns paginated tasks with server-side search and joins for human readability.
func (s *Store) ListTasksFiltered(ctx context.Context, nodeID, status, keyword string, page, pageSize int) ([]Task, int, error) {
	where := " WHERE 1 = 1"
	var args []any

	if nodeID != "" {
		where += " AND t.node_id = ?"
		args = append(args, nodeID)
	}
	if status != "" {
		if status != "queued" && status != "dispatched" && status != "succeeded" && status != "failed" && status != "rolled_back" {
			return nil, 0, errors.New("task status filter is invalid")
		}
		where += " AND t.status = ?"
		args = append(args, status)
	}
	if keyword != "" {
		pattern := "%" + strings.TrimSpace(keyword) + "%"
		where += " AND (n.name LIKE ? OR t.kind LIKE ? OR t.result_summary LIKE ? OR o.email LIKE ? OR t.id LIKE ?)"
		args = append(args, pattern, pattern, pattern, pattern, pattern)
	}

	countQuery := `SELECT COUNT(*) FROM tasks t
		LEFT JOIN nodes n ON n.id = t.node_id
		LEFT JOIN operators o ON o.id = t.created_by` + where
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count filtered tasks: %w", err)
	}

	query := `SELECT t.id, t.node_id, COALESCE(n.name, t.node_id), COALESCE(t.created_by, ''), COALESCE(o.email, ''),
		t.kind, t.idempotency_key, t.payload, t.expected_hash, t.status, COALESCE(t.result_summary, ''),
		t.created_at, COALESCE(t.started_at, 0), COALESCE(t.finished_at, 0)
		FROM tasks t
		LEFT JOIN nodes n ON n.id = t.node_id
		LEFT JOIN operators o ON o.id = t.created_by` + where + ` ORDER BY t.created_at DESC, t.id DESC LIMIT ? OFFSET ?`

	pagedArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, query, pagedArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list filtered tasks: %w", err)
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var task Task
		var createdAt, startedAt, finishedAt int64
		if err := rows.Scan(&task.ID, &task.NodeID, &task.NodeName, &task.OperatorID, &task.OperatorUsername,
			&task.Kind, &task.IdempotencyKey, &task.Payload, &task.ExpectedHash, &task.Status, &task.ResultSummary,
			&createdAt, &startedAt, &finishedAt); err != nil {
			return nil, 0, fmt.Errorf("scan task: %w", err)
		}

		task.KindName = TranslateTaskKind(task.Kind)
		task.Diagnostic = FormatTaskDiagnostic(task.Status, task.ResultSummary)
		if startedAt > 0 && finishedAt >= startedAt {
			task.DurationMs = (finishedAt - startedAt) * 1000
		}
		task.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		if startedAt != 0 {
			task.StartedAt = time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
		}
		if finishedAt != 0 {
			task.FinishedAt = time.Unix(finishedAt, 0).UTC().Format(time.RFC3339)
		}
		// Redact raw internal payload bytes on listing
		task.Payload = ""
		tasks = append(tasks, task)
	}

	return tasks, total, rows.Err()
}

// GetAuditStats calculates key indicators for the operation records dashboard.
func (s *Store) GetAuditStats(ctx context.Context) (AuditStats, error) {
	// 24 hours ago
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Unix()
	var stats AuditStats

	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE created_at >= ?`, cutoff).Scan(&stats.TodayAuditCount)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE created_at >= ?`, cutoff).Scan(&stats.TodayTaskCount)
	_ = s.db.QueryRowContext(ctx, `SELECT (
		(SELECT COUNT(*) FROM audit_events WHERE status = 'failed' AND created_at >= ?) +
		(SELECT COUNT(*) FROM tasks WHERE status IN ('failed', 'rolled_back') AND created_at >= ?)
	)`, cutoff, cutoff).Scan(&stats.FailedCount)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT operator_id) FROM audit_events WHERE created_at >= ?`, cutoff).Scan(&stats.ActiveOperators)

	return stats, nil
}
