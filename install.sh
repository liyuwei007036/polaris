#!/usr/bin/env bash
set -euo pipefail

REPOSITORY="${POLARIS_REPOSITORY:-liyuwei007036/polaris}"
VERSION="${POLARIS_VERSION:-latest}"
MODE=""
START_SERVICE=1
ALLOW_INSECURE_HTTP=0
DESTDIR="${DESTDIR:-}"
TEMP_DIR=""
SOURCE_DIR=""
ORIGINAL_ARGS=("$@")

log() {
  printf '[polaris] %s\n' "$*"
}

fail() {
  printf '[polaris] 错误：%s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
用法：install.sh [master|agent|combined] [选项]

选项：
  --version VERSION          安装指定版本；默认安装最新 Release
  --repository OWNER/REPO   GitHub 仓库；默认 liyuwei007036/polaris
  --allow-insecure-http      允许通过明文 HTTP 登录，仅适用于可信内网或测试
  --no-start                 安装文件和配置，但不启动 systemd 服务
  -h, --help                 显示帮助

Agent 可通过环境变量预先提供非交互参数：
  POLARIS_MASTER_ADDRESS
  POLARIS_MASTER_PUBKEY
  POLARIS_REGISTRATION_TOKEN

DESTDIR 仅用于将文件安装到隔离根目录；设置后不会创建用户、注册节点或调用 systemd。
EOF
}

cleanup() {
  if [[ -n "$TEMP_DIR" && -d "$TEMP_DIR" ]]; then
    rm -rf -- "$TEMP_DIR"
  fi
}
trap cleanup EXIT

while [[ $# -gt 0 ]]; do
  case "$1" in
    master|agent|combined)
      [[ -z "$MODE" ]] || fail "只能选择一种运行模式"
      MODE="$1"
      shift
      ;;
    --version)
      [[ $# -ge 2 ]] || fail "--version 缺少参数"
      VERSION="$2"
      shift 2
      ;;
    --repository)
      [[ $# -ge 2 ]] || fail "--repository 缺少参数"
      REPOSITORY="$2"
      shift 2
      ;;
    --allow-insecure-http)
      ALLOW_INSECURE_HTTP=1
      shift
      ;;
    --no-start)
      START_SERVICE=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "未知参数：$1"
      ;;
  esac
done

has_tty() {
  { exec 3</dev/tty; } 2>/dev/null || return 1
  exec 3<&-
}

choose_mode() {
  if [[ -n "$MODE" ]]; then
    return
  fi
  has_tty || fail "非交互安装必须指定 master、agent 或 combined"
  cat >/dev/tty <<'EOF'
请选择安装模式：
  1. Master
  2. Agent
  3. Combined
EOF
  local choice
  IFS= read -r -p '选择 [1-3]：' choice </dev/tty
  case "$choice" in
    1) MODE="master" ;;
    2) MODE="agent" ;;
    3) MODE="combined" ;;
    *) fail "无效的安装模式" ;;
  esac
}

root_path() {
  printf '%s%s' "$DESTDIR" "$1"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "缺少命令：$1"
}

prompt_required() {
  local variable_name="$1"
  local label="$2"
  local value="${!variable_name:-}"
  if [[ -z "$value" ]]; then
    has_tty || fail "非交互安装缺少 $label"
    IFS= read -r -p "$label：" value </dev/tty
  fi
  [[ -n "$value" ]] || fail "$label 不能为空"
  printf -v "$variable_name" '%s' "$value"
}

prompt_secret_optional() {
  local variable_name="$1"
  local label="$2"
  local value="${!variable_name:-}"
  if [[ -z "$value" ]] && has_tty; then
    IFS= read -r -s -p "$label（留空可稍后执行注册）：" value </dev/tty
    printf '\n' >/dev/tty
  fi
  printf -v "$variable_name" '%s' "$value"
}

detect_architecture() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64' ;;
    aarch64|arm64) printf 'arm64' ;;
    *) fail "不支持的 CPU 架构：$(uname -m)" ;;
  esac
}

resolve_latest_version() {
  local effective_url
  effective_url=$(curl --proto '=https' --tlsv1.2 -fsSL \
    -o /dev/null -w '%{url_effective}' \
    "https://github.com/${REPOSITORY}/releases/latest")
  local tag="${effective_url##*/}"
  [[ "$tag" == v* ]] || fail "无法确定最新 Release 版本"
  printf '%s' "${tag#v}"
}

download_release() {
  require_command curl
  require_command sha256sum
  require_command tar
  require_command mktemp
  [[ "$REPOSITORY" =~ ^[0-9A-Za-z_.-]+/[0-9A-Za-z_.-]+$ ]] || \
    fail "GitHub 仓库格式不正确：$REPOSITORY"

  local architecture
  architecture=$(detect_architecture)
  if [[ "$VERSION" == "latest" ]]; then
    VERSION=$(resolve_latest_version)
  else
    VERSION="${VERSION#v}"
  fi
  [[ "$VERSION" =~ ^[0-9A-Za-z._-]+$ ]] || fail "版本号格式不正确：$VERSION"

  local package="polaris_${VERSION}_linux_${architecture}"
  local base_url="https://github.com/${REPOSITORY}/releases/download/v${VERSION}"
  TEMP_DIR=$(mktemp -d)
  log "下载 ${package}"
  curl --proto '=https' --tlsv1.2 -fL --retry 3 \
    -o "${TEMP_DIR}/${package}.tar.gz" "${base_url}/${package}.tar.gz"
  curl --proto '=https' --tlsv1.2 -fL --retry 3 \
    -o "${TEMP_DIR}/${package}.tar.gz.sha256" "${base_url}/${package}.tar.gz.sha256"
  (
    cd "$TEMP_DIR"
    sha256sum -c "${package}.tar.gz.sha256"
    tar -xzf "${package}.tar.gz"
  )
  SOURCE_DIR="${TEMP_DIR}/${package}"
}

locate_install_source() {
  local script_path="${BASH_SOURCE[0]:-}"
  local script_dir=""
  if [[ -n "$script_path" && -f "$script_path" ]]; then
    script_dir=$(CDPATH='' cd -- "$(dirname -- "$script_path")" && pwd)
  fi
  if [[ -n "$script_dir" && -x "${script_dir}/polaris" && -d "${script_dir}/deploy" ]]; then
    SOURCE_DIR="$script_dir"
    log "使用安装包内的预编译程序"
  else
    download_release
  fi

  local required
  for required in \
    polaris \
    deploy/polaris-master.yaml \
    deploy/polaris-agent.yaml \
    deploy/polaris-master.service \
    deploy/polaris-agent.service \
    deploy/polaris-combined.service; do
    [[ -f "${SOURCE_DIR}/${required}" ]] || fail "安装包缺少 ${required}"
  done
}

reexec_with_sudo() {
  local script_path="${BASH_SOURCE[0]:-}"
  [[ -n "$script_path" && -f "$script_path" ]] || \
    fail "当前不是 root；请先把脚本保存为文件再执行：sudo bash install.sh [master|agent|combined]"
  command -v sudo >/dev/null 2>&1 || fail "当前不是 root，且系统没有 sudo；请用 root 用户执行"
  script_path=$(CDPATH='' cd -- "$(dirname -- "$script_path")" && pwd)/$(basename -- "$script_path")
  log "当前用户不是 root，改用 sudo 重新执行"
  exec sudo env \
    POLARIS_REPOSITORY="$REPOSITORY" \
    POLARIS_VERSION="$VERSION" \
    POLARIS_MASTER_ADDRESS="${POLARIS_MASTER_ADDRESS:-}" \
    POLARIS_MASTER_PUBKEY="${POLARIS_MASTER_PUBKEY:-}" \
    POLARIS_REGISTRATION_TOKEN="${POLARIS_REGISTRATION_TOKEN:-}" \
    bash "$script_path" ${ORIGINAL_ARGS[@]+"${ORIGINAL_ARGS[@]}"}
}

ensure_root() {
  [[ "$(uname -s)" == "Linux" ]] || fail "install.sh 只支持 Linux"
  if [[ -n "$DESTDIR" ]]; then
    [[ "$DESTDIR" == /* && "$DESTDIR" != "/" ]] || fail "DESTDIR 必须是非根目录的绝对路径"
    DESTDIR="${DESTDIR%/}"
    return
  fi
  [[ "$EUID" -eq 0 ]] || reexec_with_sudo
  require_command systemctl
  require_command install
}

ensure_master_user() {
  if [[ -n "$DESTDIR" ]]; then
    return 0
  fi
  if ! getent group polaris >/dev/null 2>&1; then
    groupadd --system polaris
  fi
  if ! id polaris >/dev/null 2>&1; then
    local nologin_shell
    nologin_shell=$(command -v nologin || true)
    [[ -n "$nologin_shell" ]] || nologin_shell="/usr/sbin/nologin"
    useradd --system --gid polaris --home-dir /var/lib/polaris-master \
      --shell "$nologin_shell" polaris
  fi
}

install_directory() {
  local mode="$1"
  local owner="$2"
  local group="$3"
  local directory="$4"
  if [[ -n "$DESTDIR" ]]; then
    install -d -m "$mode" "$(root_path "$directory")"
  else
    install -d -o "$owner" -g "$group" -m "$mode" "$directory"
  fi
}

install_common_files() {
  install -D -m 0755 "${SOURCE_DIR}/polaris" "$(root_path /usr/local/bin/polaris)"
  install -D -m 0644 "${SOURCE_DIR}/deploy/polaris-master.service" \
    "$(root_path /etc/systemd/system/polaris-master.service)"
  install -D -m 0644 "${SOURCE_DIR}/deploy/polaris-agent.service" \
    "$(root_path /etc/systemd/system/polaris-agent.service)"
  install -D -m 0644 "${SOURCE_DIR}/deploy/polaris-combined.service" \
    "$(root_path /etc/systemd/system/polaris-combined.service)"
}

configure_master() {
  ensure_master_user
  install_directory 0700 polaris polaris /var/lib/polaris-master
  install_directory 0750 root polaris /etc/polaris
  local target
  target=$(root_path /etc/polaris/master.yaml)
  if [[ ! -e "$target" ]]; then
    install -m 0640 "${SOURCE_DIR}/deploy/polaris-master.yaml" "$target"
    if [[ "$ALLOW_INSECURE_HTTP" -eq 1 ]]; then
      sed -i 's/^allow_insecure_http:.*/allow_insecure_http: true/' "$target"
    fi
    if [[ -z "$DESTDIR" ]]; then
      chown root:polaris "$target"
    fi
  else
    log "保留已有配置：/etc/polaris/master.yaml"
  fi
}

write_agent_config() {
  local target="$1"
  local master_address="$2"
  local master_pubkey="$3"
  local temporary="${target}.tmp.$$"
  umask 077
  cat >"$temporary" <<EOF
data_dir: /var/lib/polaris-agent
master_address: '${master_address}'
master_public_key: '${master_pubkey}'
heartbeat_interval: 30s
connections_interval: 10s
EOF
  chmod 0600 "$temporary"
  mv "$temporary" "$target"
}

ensure_agent_dependencies() {
  if [[ -n "$DESTDIR" ]]; then
    return 0
  fi
  if ! command -v traceroute >/dev/null 2>&1 || ! command -v logrotate >/dev/null 2>&1; then
    log "安装 Agent 基础依赖 (traceroute, logrotate)..."
    if command -v apt-get >/dev/null 2>&1; then
      DEBIAN_FRONTEND=noninteractive apt-get update -qq && \
      DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends traceroute iproute2 iputils-ping logrotate || true
    elif command -v dnf >/dev/null 2>&1; then
      dnf install -y traceroute iproute iputils logrotate || true
    elif command -v yum >/dev/null 2>&1; then
      yum install -y traceroute iproute iputils logrotate || true
    elif command -v apk >/dev/null 2>&1; then
      apk add traceroute iproute2 iputils logrotate || true
    fi
  fi
}

configure_node_performance_and_logs() {
  if [[ -n "$DESTDIR" ]]; then
    return 0
  fi

  log "配置系统日志限制 (systemd-journald 内存上限 20MB / 磁盘 50MB)..."
  mkdir -p /etc/systemd/journald.conf.d
  cat <<'EOF' >/etc/systemd/journald.conf.d/polaris-journald.conf
[Journal]
SystemMaxUse=50M
RuntimeMaxUse=20M
MaxRetentionSec=3day
EOF
  if command -v journalctl >/dev/null 2>&1; then
    journalctl --vacuum-size=50M >/dev/null 2>&1 || true
  fi
  if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet systemd-journald 2>/dev/null; then
    systemctl restart systemd-journald >/dev/null 2>&1 || true
  fi

  log "配置 sing-box 业务日志自动轮转与压缩归档 (10MB 自动轮转)..."
  mkdir -p /etc/logrotate.d
  cat <<'EOF' >/etc/logrotate.d/sing-box
/var/log/sing-box/*.log {
    size 10M
    rotate 3
    missingok
    notifempty
    compress
    copytruncate
}
EOF

  log "配置 sing-box 进程特权与 OOM 保护 (drop-in 覆盖)..."
  mkdir -p /etc/systemd/system/sing-box.service.d
  cat <<'EOF' >/etc/systemd/system/sing-box.service.d/10-polaris-priority.conf
[Service]
Nice=-10
CPUWeight=1000
OOMScoreAdjust=-900
LimitNOFILE=1048576
EOF
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    if systemctl is-active --quiet sing-box.service 2>/dev/null; then
      systemctl restart sing-box.service >/dev/null 2>&1 || true
    fi
  fi

  local mem_total_kb swap_total_kb
  mem_total_kb=$(awk '$1 == "MemTotal:" { print $2 }' /proc/meminfo 2>/dev/null || echo 0)
  swap_total_kb=$(awk '$1 == "SwapTotal:" { print $2 }' /proc/meminfo 2>/dev/null || echo 0)
  if [[ "$mem_total_kb" -gt 0 && "$mem_total_kb" -le 1048576 && "$swap_total_kb" -eq 0 ]]; then
    if [[ ! -e /swapfile ]]; then
      log "检测到低内存主机 (<=1GB) 且未配置 Swap，自动初始化 1GB 虚拟 Swap 缓冲垫..."
      if command -v fallocate >/dev/null 2>&1; then
        fallocate -l 1G /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=1024 status=none 2>/dev/null || true
      else
        dd if=/dev/zero of=/swapfile bs=1M count=1024 status=none 2>/dev/null || true
      fi
      if [[ -e /swapfile ]]; then
        chmod 600 /swapfile
        mkswap /swapfile >/dev/null 2>&1 || true
        if swapon /swapfile >/dev/null 2>&1; then
          if ! grep -q '/swapfile' /etc/fstab 2>/dev/null; then
            echo '/swapfile none swap sw 0 0' >> /etc/fstab
          fi
        fi
      fi
    fi
  fi

  log "优化网络连接与 UDP 缓冲区参数 (8MB UDP 缓存 / 队列扩容)..."
  mkdir -p /etc/sysctl.d
  cat <<'EOF' >/etc/sysctl.d/99-polaris-tuning.conf
net.core.rmem_max = 8388608
net.core.wmem_max = 8388608
net.ipv4.udp_rmem_min = 16384
net.ipv4.udp_wmem_min = 16384
net.core.somaxconn = 8192
net.ipv4.tcp_max_syn_backlog = 8192
net.ipv4.tcp_tw_reuse = 1
vm.swappiness = 10
EOF
  if command -v sysctl >/dev/null 2>&1; then
    sysctl --system >/dev/null 2>&1 || sysctl -p /etc/sysctl.d/99-polaris-tuning.conf >/dev/null 2>&1 || true
  fi

  if command -v systemctl >/dev/null 2>&1; then
    if systemctl is-active --quiet fwupd 2>/dev/null || systemctl is-enabled --quiet fwupd 2>/dev/null; then
      log "停用云 VPS 冗余高耗内存服务 (fwupd)..."
      systemctl disable --now fwupd >/dev/null 2>&1 || true
    fi
  fi
}

configure_agent() {
  ensure_agent_dependencies
  configure_node_performance_and_logs
  local master_address="$1"
  local master_pubkey="$2"
  install_directory 0700 root root /var/lib/polaris-agent
  if [[ -e "$(root_path /etc/polaris/master.yaml)" ]] && \
    { [[ -n "$DESTDIR" ]] || getent group polaris >/dev/null 2>&1; }; then
    install_directory 0750 root polaris /etc/polaris
  else
    install_directory 0700 root root /etc/polaris
  fi
  local target
  target=$(root_path /etc/polaris/agent.yaml)
  if [[ ! -e "$target" ]]; then
    [[ -n "$master_address" ]] || fail "Master 地址不能为空"
    [[ -n "$master_pubkey" ]] || fail "Master 公钥不能为空"
    [[ "$master_address" == *:* && "$master_address" != *[[:space:]]* && \
      "$master_address" != *"'"* ]] || fail "Master 地址必须是无空白的主机:端口"
    [[ "$master_pubkey" =~ ^[0-9A-Za-z+/]{43}=$ ]] || \
      fail "Master Noise 公钥必须是 32 字节 Base64 字符串"
    write_agent_config "$target" "$master_address" "$master_pubkey"
  else
    log "保留已有配置：/etc/polaris/agent.yaml"
  fi
}

master_public_key() {
  local key
  key=$(/usr/local/bin/polaris master show-pubkey --config /etc/polaris/master.yaml)
  chown -R polaris:polaris /var/lib/polaris-master
  printf '%s' "$key"
}

configured_agent_port() {
  local port
  port=$(awk '$1 == "agent_port:" { print $2; exit }' /etc/polaris/master.yaml)
  printf '%s' "${port:-19994}"
}

register_agent_if_requested() {
  local token="${POLARIS_REGISTRATION_TOKEN:-}"
  if [[ -e /var/lib/polaris-agent/agent-noise.key ]]; then
    log "检测到现有 Agent 身份，跳过重复注册"
    return
  fi
  prompt_secret_optional token '请输入一次性注册令牌'
  if [[ -n "$token" ]]; then
    /usr/local/bin/polaris agent register \
      --config /etc/polaris/agent.yaml --token "$token"
  else
    log "尚未注册 Agent；安装完成后请执行："
    log "sudo /usr/local/bin/polaris agent register --config /etc/polaris/agent.yaml --token TOKEN"
  fi
}

start_selected_service() {
  local selected="polaris-${MODE}.service"
  local other
  for other in polaris-master.service polaris-agent.service polaris-combined.service; do
    if [[ "$other" != "$selected" ]] && systemctl is-active --quiet "$other"; then
      fail "${other} 正在运行；请先停止它，避免同时启动多个模式"
    fi
    if [[ "$other" != "$selected" ]]; then
      systemctl disable "$other" >/dev/null 2>&1 || true
    fi
  done
  systemctl daemon-reload
  systemctl enable "$selected" >/dev/null
  systemctl restart "$selected"
  systemctl is-active --quiet "$selected" || fail "${selected} 启动失败，请检查 journalctl"
  log "${selected} 已启动"
}

install_master_mode() {
  configure_master
  if [[ -z "$DESTDIR" ]]; then
    local public_key
    public_key=$(master_public_key)
    if [[ "$START_SERVICE" -eq 1 ]]; then
      start_selected_service
    fi
    log "Master Noise 公钥：${public_key}"
    log "默认控制台端口：19670；首次登录账户 polaris_admin / 123456"
  fi
}

install_agent_mode() {
  local master_address="${POLARIS_MASTER_ADDRESS:-}"
  local master_pubkey="${POLARIS_MASTER_PUBKEY:-}"
  if [[ ! -e "$(root_path /etc/polaris/agent.yaml)" ]]; then
    prompt_required master_address 'Master 地址（主机:端口）'
    prompt_required master_pubkey 'Master Noise 公钥'
  fi
  configure_agent "$master_address" "$master_pubkey"
  if [[ -z "$DESTDIR" ]]; then
    register_agent_if_requested
    if [[ "$START_SERVICE" -eq 1 ]]; then
      start_selected_service
    fi
  fi
}

install_combined_mode() {
  configure_master
  if [[ -n "$DESTDIR" ]]; then
    fail "Combined 的 DESTDIR 安装需要实际二进制生成 Master 公钥，当前仅支持真实系统安装"
  fi
  local public_key
  local agent_port
  public_key=$(master_public_key)
  agent_port=$(configured_agent_port)
  configure_agent "127.0.0.1:${agent_port}" "$public_key"
  if [[ "$START_SERVICE" -eq 1 ]]; then
    start_selected_service
    log "Combined 已启动。请登录控制台创建一次性注册令牌。"
  fi
  register_agent_if_requested
  log "Master Noise 公钥：${public_key}"
}

ensure_root
choose_mode
locate_install_source
install_common_files

case "$MODE" in
  master) install_master_mode ;;
  agent) install_agent_mode ;;
  combined) install_combined_mode ;;
esac

log "安装完成（模式：${MODE}）"
