#!/usr/bin/env bash
# go-vless 服务器一键安装脚本 (Linux + systemd)
#
# 安装 (自动解析最新版, 已有配置则保留):
#   curl -fsSL https://raw.githubusercontent.com/Atticus6/go-vless/main/install.sh | sudo bash
# 本地:
#   sudo ./install.sh [install|update|uninstall] [version]
# 环境变量可覆盖默认值: UUID PORT TUNNEL TUNNEL_PROTO MAX_CONN CONFIG_KEY DOMAIN RUNTIME
# 其中 TUNNEL 不设时安装会交互询问 (默认开启); RUNTIME=binary|docker 指定运行模式,
# 不设且本机有 Docker 时交互询问 (默认二进制), 无 Docker 直接二进制.
# Docker 模式镜像: ghcr.io/atticus6/go-vless:<版本去v>, 随 release 版本走 (update 可升级).
set -euo pipefail

REPO="Atticus6/go-vless"
# GHCR 仓库名必须全小写
IMAGE="ghcr.io/$(printf "%s" "$REPO" | tr '[:upper:]' '[:lower:]')"
BIN="/usr/local/bin/go-vless"
ENV_FILE="/etc/go-vless.env"
UNIT="/etc/systemd/system/go-vless.service"
COMPOSE_DIR="${COMPOSE_DIR:-/opt/go-vless}"

ACTION="${1:-install}"
VERSION="${2:-${VERSION:-latest}}"

need_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "请用 root 运行 (sudo bash)" >&2
    exit 1
  fi
  for c in curl tar sha256sum systemctl; do
    command -v "$c" >/dev/null 2>&1 || {
      echo "缺少依赖: $c" >&2
      exit 1
    }
  done
}

# 最新 release tag
latest_tag() {
  local tag
  tag="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
    grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
  if [ -z "$tag" ]; then
    echo "获取最新版本失败" >&2
    exit 1
  fi
  echo "$tag"
}

goarch() {
  case "$(uname -m)" in
    x86_64 | amd64) echo "amd64" ;;
    aarch64 | arm64) echo "arm64" ;;
    *)
      echo "不支持的架构: $(uname -m)" >&2
      exit 1
      ;;
  esac
}

rand_hex() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 16
  else
    head -c16 /dev/urandom | od -An -tx1 | tr -d ' \n'
    echo
  fi
}

# 下载 + 验签 + 安装二进制
install_bin() {
  local ver="$1"
  local arch
  arch="$(goarch)"
  local asset="go-vless-${ver}-linux-${arch}.tar.gz"
  local base="https://github.com/$REPO/releases/download/${ver}"

  local tmp
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf \"$tmp\"" RETURN

  echo "下载 $asset ..."
  curl -fsSL -o "$tmp/$asset" "$base/$asset"
  curl -fsSL -o "$tmp/SHA256SUMS.txt" "$base/SHA256SUMS.txt"
  (cd "$tmp" && grep -F -- "$asset" SHA256SUMS.txt | sha256sum -c -) ||
    {
      echo "sha256 校验失败" >&2
      exit 1
    }

  tar -xzf "$tmp/$asset" -C "$tmp"
  install -m 0755 "$tmp/go-vless" "$BIN"
  echo "二进制已安装: $BIN"
}

# compose 可用命令 (v2 插件优先, 兼容 v1 独立二进制), 无则返回非零
compose_bin() {
  if docker compose version >/dev/null 2>&1; then
    printf "docker compose"
    return 0
  fi
  if command -v docker-compose >/dev/null 2>&1; then
    printf "docker-compose"
    return 0
  fi
  return 1
}

# 本机 Docker 是否可用 (CLI + daemon + compose 三者齐全)
docker_available() {
  command -v docker >/dev/null 2>&1 || return 1
  docker info >/dev/null 2>&1 || return 1
  [ -n "$(compose_bin)" ] || return 1
}

# 运行模式: RUNTIME 环境变量优先, 否则有 Docker 时交互询问 (默认二进制).
# 非终端环境不问直接二进制 (避免 read 卡死).
ask_runtime() {
  local r
  r="$(printf "%s" "${RUNTIME:-}" | tr '[:upper:]' '[:lower:]')"
  case "$r" in
    docker | compose)
      printf "docker"
      return
      ;;
    binary | systemd | "") : ;;
    *)
      echo "RUNTIME 非法: $RUNTIME (binary|docker)" >&2
      exit 1
      ;;
  esac
  if docker_available && { [ -t 0 ] || [ -t 1 ]; }; then
    local ans=""
    if [ -t 0 ]; then
      printf "检测到 Docker, 选择运行方式 [1=二进制(默认)/2=Docker]: "
      read -r ans 2>/dev/null || ans=""
    elif [ -e /dev/tty ]; then
      printf "检测到 Docker, 选择运行方式 [1=二进制(默认)/2=Docker]: " >/dev/tty || true
      read -r ans </dev/tty 2>/dev/null || ans=""
    fi
    case "$ans" in
      2 | [Dd]ocker | [Cc]ompose)
        printf "docker"
        return
        ;;
    esac
  fi
  printf "binary"
}

# 写 compose 文件 (端口与容器内监听端口取 env 文件同一值, 双端 baked 一致)
write_compose() {
  local imgtag="${1#v}" port="$2"
  mkdir -p "$COMPOSE_DIR"
  cat >"$COMPOSE_DIR/docker-compose.yml" <<EOF
# go-vless Docker Compose (install.sh 生成)
# 改端口: 同改本文件 ports 映射与 $ENV_FILE 里 PORT (容器内监听端口), 然后 up -d
services:
  go-vless:
    image: $IMAGE:$imgtag
    container_name: go-vless
    restart: unless-stopped
    ports:
      - "$port:$port"
    env_file:
      - $ENV_FILE
EOF
  echo "compose 已生成: $COMPOSE_DIR/docker-compose.yml ($IMAGE:$imgtag)"
}

compose_up() {
  local compose
  compose="$(compose_bin)" || {
    echo "docker compose 不可用" >&2
    exit 1
  }
  # shellcheck disable=SC2086
  (cd "$COMPOSE_DIR" && $compose up -d)
}

compose_down() {
  [ -f "$COMPOSE_DIR/docker-compose.yml" ] || return 0
  local compose
  compose="$(compose_bin 2>/dev/null)" || {
    echo "docker compose 不可用, 跳过容器清理 ($COMPOSE_DIR 保留)"
    return 0
  }
  # shellcheck disable=SC2086
  (cd "$COMPOSE_DIR" && $compose down 2>/dev/null) || true
}
# 隧道开关: TUNNEL 环境变量优先, 否则交互询问 (默认开启, 回车即 Y).
# 只有 stdin 或 stdout 接了终端才问:
#   直接执行 -> stdin 是终端, 直接问;
#   curl 管道安装 -> stdin 是脚本流, 但 stdout 是终端, 改从 /dev/tty 问;
#   CI/定时任务等无终端环境 -> 不问, 直接默认 (避免 read 卡死).
ask_tunnel() {
  if [ -n "${TUNNEL:-}" ]; then
    printf "%s" "$TUNNEL"
    return
  fi
  local ans=""
  if [ -t 0 ]; then
    printf "是否启动 Argo 隧道? [Y/n]: "
    read -r ans 2>/dev/null || ans=""
  elif [ -t 1 ] && [ -e /dev/tty ]; then
    printf "是否启动 Argo 隧道? [Y/n]: " >/dev/tty || true
    read -r ans </dev/tty 2>/dev/null || ans=""
  fi
  case "$ans" in
    [Nn] | [Nn][Oo] | 0 | [Ff]*) printf "0" ;;
    *) printf "1" ;;
  esac
}

# 写配置 (已存在则保留, 避免重装刷掉 UUID/密钥)
write_env() {
  if [ -f "$ENV_FILE" ]; then
    echo "配置已存在, 保留: $ENV_FILE"
    return
  fi
  local tunnel
  tunnel="$(ask_tunnel)"
  cat >"$ENV_FILE" <<EOF
# go-vless 配置 (install.sh 生成, 手改后 systemctl restart go-vless 生效)
UUID=${UUID:-$(cat /proc/sys/kernel/random/uuid)}
PORT=${PORT:-8080}
TUNNEL=$tunnel
TUNNEL_PROTO=${TUNNEL_PROTO:-auto}
MAX_CONN=${MAX_CONN:-4096}
CONFIG_KEY=${CONFIG_KEY:-$(rand_hex)}
DOMAIN=${DOMAIN:-}
EOF
  chmod 600 "$ENV_FILE"
  echo "配置已生成: $ENV_FILE"
}

write_unit() {
  cat >"$UNIT" <<EOF
[Unit]
Description=go-vless VLESS over WebSocket server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$ENV_FILE
ExecStart=$BIN
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
}

show_info() {
  local mode="${1:-binary}"
  # shellcheck disable=SC1090
  . "$ENV_FILE" 2>/dev/null || true
  echo "----------------------------------------"
  if [ "$mode" = "docker" ]; then
    (cd "$COMPOSE_DIR" && $(compose_bin 2>/dev/null || echo "docker compose") ps 2>/dev/null) || true
  else
    systemctl --no-pager --full status go-vless 2>/dev/null | head -12 || true
  fi
  echo "----------------------------------------"
  echo "运行模式:   $mode"
  echo "UUID:       ${UUID:-}"
  echo "CONFIG_KEY: ${CONFIG_KEY:-}"
  echo "管理页:     http://<服务器IP>:${PORT:-8080}/config?key=${CONFIG_KEY:-}"
  if [ "$mode" = "docker" ]; then
    echo "启停:       cd $COMPOSE_DIR && docker compose [up -d|down|restart|ps]"
    echo "日志:       docker logs -f go-vless"
  else
    echo "启停:       systemctl [start|stop|restart|status] go-vless"
    echo "日志:       journalctl -u go-vless -f"
  fi
}

do_install() {
  need_root
  local ver="$VERSION"
  if [ "$ver" = "latest" ]; then
    ver="$(latest_tag)"
  fi
  local mode
  mode="$(ask_runtime)"
  echo "运行模式: $mode"
  write_env
  if [ "$mode" = "docker" ]; then
    # 切到 docker: 停掉二进制服务 (如有)
    systemctl disable --now go-vless 2>/dev/null || true
    rm -f "$UNIT"
    systemctl daemon-reload 2>/dev/null || true
    # shellcheck disable=SC1090
    . "$ENV_FILE"
    write_compose "$ver" "${PORT:-8080}"
    compose_up
    sleep 3
    show_info docker
  else
    # 切回二进制: 停掉 compose (如有)
    compose_down
    rm -rf "$COMPOSE_DIR"
    install_bin "$ver"
    write_unit
    systemctl enable --now go-vless
    sleep 2
    show_info binary
  fi
}

do_update() {
  need_root
  local ver="$VERSION"
  if [ "$ver" = "latest" ]; then
    ver="$(latest_tag)"
  fi
  if [ -f "$COMPOSE_DIR/docker-compose.yml" ]; then
    [ -f "$ENV_FILE" ] || {
      echo "配置丢失 ($ENV_FILE), 先执行安装" >&2
      exit 1
    }
    # shellcheck disable=SC1090
    . "$ENV_FILE"
    write_compose "$ver" "${PORT:-8080}"
    local compose
    compose="$(compose_bin)" || {
      echo "docker compose 不可用" >&2
      exit 1
    }
    # shellcheck disable=SC2086
    (cd "$COMPOSE_DIR" && $compose pull && $compose up -d)
    sleep 3
    show_info docker
  elif [ -x "$BIN" ]; then
    install_bin "$ver"
    systemctl daemon-reload
    systemctl restart go-vless
    sleep 2
    show_info binary
  else
    echo "尚未安装, 先执行安装" >&2
    exit 1
  fi
}

do_uninstall() {
  need_root
  systemctl disable --now go-vless 2>/dev/null || true
  compose_down
  rm -rf "$COMPOSE_DIR"
  rm -f "$UNIT" "$BIN"
  systemctl daemon-reload
  echo "已卸载 (配置保留在 $ENV_FILE, 不要了可手动删除)"
}

# 直接执行时跑主流程, 被 source 时仅加载函数 (方便测试 ask_tunnel 等)
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  case "$ACTION" in
    install) do_install ;;
    update) do_update ;;
    uninstall) do_uninstall ;;
    *)
      echo "用法: $0 [install|update|uninstall] [version]" >&2
      exit 1
      ;;
  esac
fi
