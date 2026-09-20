#!/usr/bin/env bash
# go-vless 服务器一键安装脚本 (Linux + systemd)
# One-click installer for go-vless (Linux + systemd).
#
# 提示信息语言 / Message language: LC_ALL/LANG 以 zh 开头即中文, 否则英文;
# starts with zh = Chinese, otherwise English. GO_VLESS_LANG=zh|en 可强制指定.
#
# 安装 (自动解析最新版, 已有配置则保留):
#   curl -fsSL https://raw.githubusercontent.com/Atticus6/go-vless/main/install.sh | sudo bash
# 管道安装 + 注册到 dashboard (按节点复制安装命令, 三元组 服务端地址:节点id:config_key):
#   curl -fsSL https://raw.githubusercontent.com/Atticus6/go-vless/main/install.sh | sudo bash -s -- --register "<服务端地址>:<节点id>:<config_key>"
# 本地:
#   sudo ./install.sh [--register <三元组>] [install|update|uninstall] [version]
# 装完脚本会存一份到 /usr/local/bin/go-vless-install.sh，后续直接 sudo go-vless-install.sh [update|uninstall]
# 环境变量可覆盖默认值: UUID PORT TUNNEL TUNNEL_PROTO MAX_CONN CONFIG_KEY DOMAIN SSL_DOMAIN RUNTIME REGISTER
# 其中 REGISTER 与 --register 等价 (flag 优先), 内容为 服务端地址:节点id:config_key;
# 不填则只启动代理服务, 不向 dashboard 注册上报.
# 其中 TUNNEL 不设时安装会交互询问 (默认开启, 重装默认保持现有值);
# RUNTIME=binary|docker 指定运行模式 (重装同样默认保持现有模式),
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
# 安装脚本自身存档: 装完/升级后可直接 sudo go-vless-install.sh [update|uninstall]
INSTALLER_DEST="/usr/local/bin/go-vless-install.sh"
INSTALLER_URL="https://raw.githubusercontent.com/$REPO/main/install.sh"

# ---- i18n ----
# 本脚本所有面向用户的输出跟随系统语言: LC_ALL/LANG 以 zh 开头即中文,
# 否则英文; GO_VLESS_LANG=zh|en 可强制指定 (与 go-vless --lang 对应).
_detect_lang() {
  case "${GO_VLESS_LANG:-}" in
    zh* | ZH*) printf "zh"; return ;;
    en* | EN* | C | POSIX) printf "en"; return ;;
  esac
  case "${LC_ALL:-${LANG:-}}" in
    zh* | ZH*) printf "zh" ;;
    *) printf "en" ;;
  esac
}
VLESS_LANG="$(_detect_lang)"
unset -f _detect_lang

# T <key> [args...]: 取双语文案并 printf 格式化输出 (不带换行, 调用方自行加).
T() {
  local key="$1"
  shift || true
  local zh="" en=""
  case "$key" in
    reg_missing_arg) zh="--register 缺少参数 (格式 服务端地址:节点id:config_key)"; en="--register requires an argument (format server-url:node-id:config_key)" ;;
    reg_bad_format) zh="REGISTER 格式错误，应为 服务端地址:节点id:config_key"; en="bad REGISTER format, want server-url:node-id:config_key" ;;
    need_root) zh="请用 root 运行 (sudo bash)"; en="please run as root (sudo bash)" ;;
    missing_dep) zh="缺少依赖: %s"; en="missing dependency: %s" ;;
    latest_fail) zh="获取最新版本失败"; en="failed to resolve latest release" ;;
    latest_hint) zh="可手动指定版本重试，如：sudo ./install.sh update v0.1.1"; en="retry with an explicit version, e.g.: sudo ./install.sh update v0.1.1" ;;
    bad_arch) zh="不支持的架构: %s"; en="unsupported architecture: %s" ;;
    downloading) zh="下载 %s ..."; en="downloading %s ..." ;;
    sha_fail) zh="sha256 校验失败"; en="sha256 verification failed" ;;
    bin_installed) zh="二进制已安装: %s"; en="binary installed: %s" ;;
    runtime_invalid) zh="RUNTIME 非法: %s (binary|docker)"; en="invalid RUNTIME: %s (binary|docker)" ;;
    ask_runtime) zh="检测到 Docker, 选择运行方式 [1=二进制(默认)/2=Docker]: "; en="Docker detected, choose runtime [1=binary(default)/2=Docker]: " ;;
    ask_runtime_cur) zh="检测到 Docker, 选择运行方式 [1=二进制/2=Docker] (当前: %s, 回车保持): "; en="Docker detected, choose runtime [1=binary/2=Docker] (current: %s, Enter to keep): " ;;
    compose_written) zh="compose 已生成: %s (%s)"; en="compose written: %s (%s)" ;;
    compose_missing) zh="docker compose 不可用"; en="docker compose not available" ;;
    compose_skip) zh="docker compose 不可用, 跳过容器清理 (%s 保留)"; en="docker compose not available, skip container cleanup (keeping %s)" ;;
    ask_tunnel) zh="是否启动 Argo 隧道? [Y/n]: "; en="Enable Argo tunnel? [Y/n]: " ;;
    env_kept) zh="配置已存在, 保留: %s"; en="config exists, keeping: %s" ;;
    env_written) zh="配置已生成: %s"; en="config written: %s" ;;
    reg_updated) zh="注册信息已更新 (dashboard: %s)"; en="register info updated (dashboard: %s)" ;;
    env_missing) zh="配置不存在 (%s), 先执行安装"; en="config not found (%s), install first" ;;
    env_lost) zh="配置丢失 (%s), 先执行安装"; en="config lost (%s), install first" ;;
    not_installed) zh="尚未安装, 先执行安装"; en="not installed yet, install first" ;;
    uninstalled) zh="已卸载 (配置保留在 %s, 不要了可手动删除)"; en="uninstalled (config kept at %s, delete manually if unneeded)" ;;
    usage) zh="用法: %s [--register <服务端地址:节点id:config_key>] [install|update|uninstall] [version]"; en="usage: %s [--register <server-url:node-id:config_key>] [install|update|uninstall] [version]" ;;
    mode) zh="运行模式:   %s"; en="Runtime:     %s" ;;
    dash_none) zh="未配置 (仅本地运行，不向 dashboard 注册)"; en="not set (local only, no dashboard registration)" ;;
    info_uuid) zh="UUID:       %s"; en="UUID:         %s" ;;
    info_key) zh="CONFIG_KEY: %s"; en="CONFIG_KEY:   %s" ;;
    info_dash) zh="Dashboard:  %s"; en="Dashboard:    %s" ;;
    info_node) zh="节点 ID:    %s"; en="Node ID:      %s" ;;
    info_tunnel) zh="隧道:       %s"; en="Tunnel:      %s" ;;
    docker_boot) zh="Docker 开机自启已设置"; en="Docker autostart enabled" ;;
    tun_on) zh="开"; en="on" ;;
    tun_off) zh="关"; en="off" ;;
    installer_saved) zh="安装脚本已保存至 %s，后续 update/uninstall 可直接用它"; en="installer saved to %s, use it for future update/uninstall" ;;
    installer_save_fail) zh="安装脚本存档失败 (%s)，不影响本次安装"; en="failed to archive installer (%s), continuing anyway" ;;
    confirm_reinstall) zh="检测到已安装 (%s)，覆盖重装？[Y/n] "; en="already installed (%s), overwrite? [Y/n] " ;;
    reinstall_abort) zh="已取消（用 update 升级，或 uninstall 后重装）"; en="aborted (use update to upgrade, or uninstall first)" ;;
    auto_proceed) zh="非交互环境，%s 已存在，自动继续"; en="non-interactive, %s exists, continuing automatically" ;;
    info_ssl) zh="HTTPS 证书: %s"; en="HTTPS cert:  %s" ;;
    ssl_off) zh="未启用 (仅 HTTP)"; en="disabled (HTTP only)" ;;
    info_admin) zh="管理页:     http://<服务器IP>:%s/config?key=%s"; en="Admin page:  http://<server-IP>:%s/config?key=%s" ;;
    info_ctl) zh="启停:       %s"; en="Control:     %s" ;;
    info_logs) zh="日志:       %s"; en="Logs:        %s" ;;
    running_mode) zh="运行模式: %s"; en="runtime: %s" ;;
    *) zh="$key"; en="$key" ;;
  esac
  if [ "$VLESS_LANG" = "zh" ]; then
    # shellcheck disable=SC2059
    printf -- "$zh" "$@"
  else
    # shellcheck disable=SC2059
    printf -- "$en" "$@"
  fi
}

# 命令行参数预处理: 摘出 --register (管道安装 sudo bash -s -- 透参就靠它,
# 纯 env 变量过不了 sudo), 剩下的 positional 原样放回 $1/$2.
REGISTER="${REGISTER:-}"
_ARGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --register)
      [ $# -ge 2 ] || {
        T reg_missing_arg >&2
        echo >&2
        exit 1
      }
      REGISTER="$2"
      shift 2
      ;;
    --register=*)
      REGISTER="${1#--register=}"
      shift
      ;;
    --)
      shift
      while [ $# -gt 0 ]; do
        _ARGS+=("$1")
        shift
      done
      break
      ;;
    *)
      _ARGS+=("$1")
      shift
      ;;
  esac
done
if [ "${#_ARGS[@]}" -gt 0 ]; then
  set -- "${_ARGS[@]}"
else
  set --
fi
unset _ARGS
ACTION="${1:-install}"
VERSION="${2:-${VERSION:-latest}}"

# REGISTER 三元组 服务端地址:节点id:config_key (服务端地址自带冒号, 从右拆;
# 节点 id 为 UUID、key 为 hex, 均不含冒号). 拆出的 key 直接作为 CONFIG_KEY.
parse_register() {
  local triple="$1" key rest id url
  key="${triple##*:}"
  rest="${triple%:*}"
  id="${rest##*:}"
  url="${rest%:*}"
  if [ -z "$url" ] || [ -z "$id" ] || [ -z "$key" ]; then
    return 1
  fi
  case "$url" in
    http://* | https://*) ;;
    *) return 1 ;;
  esac
  case "$id" in
    ????????-????-????-????-????????????) ;;
    *) return 1 ;;
  esac
  printf "%s\n%s\n%s\n" "$url" "$id" "$key"
}

if [ -n "${REGISTER:-}" ]; then
  _reg="$(parse_register "$REGISTER")" || {
    T reg_bad_format >&2
    echo >&2
    exit 1
  }
  REGISTER_URL="$(printf "%s" "$_reg" | sed -n '1p')"
  REGISTER_NODE_ID="$(printf "%s" "$_reg" | sed -n '2p')"
  CONFIG_KEY="$(printf "%s" "$_reg" | sed -n '3p')"
  export REGISTER_URL REGISTER_NODE_ID CONFIG_KEY
  unset _reg
fi

need_root() {
  if [ "$(id -u)" -ne 0 ]; then
    T need_root >&2
    echo >&2
    exit 1
  fi
  for c in curl tar sha256sum systemctl; do
    command -v "$c" >/dev/null 2>&1 || {
      T missing_dep "$c" >&2
      echo >&2
      exit 1
    }
  done
}

# 最新 release tag: 先调 GitHub API, 被限流/403 时改走 releases/latest
# 页面跳转解析 (该接口不计 API 配额); 都失败则报错并提示手动指定版本.
latest_tag() {
  local tag eff
  tag="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null |
    grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
  if [ -z "$tag" ]; then
    eff="$(curl -fsSIL -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" 2>/dev/null)" || eff=""
    tag="$(printf "%s" "$eff" | sed -nE 's|.*/tag/(v[^/]+).*|\1|p')"
  fi
  case "$tag" in
    v*)
      echo "$tag"
      return 0
      ;;
  esac
  T latest_fail >&2
  echo >&2
  T latest_hint >&2
  echo >&2
  exit 1
}

goarch() {
  case "$(uname -m)" in
    x86_64 | amd64) echo "amd64" ;;
    aarch64 | arm64) echo "arm64" ;;
    *)
      T bad_arch "$(uname -m)" >&2
      echo >&2
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

  T downloading "$asset"
  echo
  curl -fsSL -o "$tmp/$asset" "$base/$asset"
  curl -fsSL -o "$tmp/SHA256SUMS.txt" "$base/SHA256SUMS.txt"
  (cd "$tmp" && grep -F -- "$asset" SHA256SUMS.txt | sha256sum -c -) ||
    {
      T sha_fail >&2
      echo >&2
      exit 1
    }

  tar -xzf "$tmp/$asset" -C "$tmp"
  install -m 0755 "$tmp/go-vless" "$BIN"
  T bin_installed "$BIN"
  echo
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

# 当前运行模式 (重装时作默认值): compose 文件在即 docker,
# 否则有二进制/service 痕迹即 binary, 全无即全新安装 ("").
current_mode() {
  [ -f "$COMPOSE_DIR/docker-compose.yml" ] && {
    printf "docker"
    return
  }
  { [ -f "$UNIT" ] || [ -x "$BIN" ]; } && {
    printf "binary"
    return
  }
  printf ""
}

# 运行模式: RUNTIME 环境变量优先, 否则交互询问.
# stdin TTY 优先, 管道安装且 stdout 是终端时走 /dev/tty;
# 拿不到终端时取当前模式 (全新安装则二进制), 避免重装静默翻转模式.
ask_runtime() {
  local cur="${1:-}"
  local r
  r="$(printf "%s" "${RUNTIME:-}" | tr '[:upper:]' '[:lower:]')"
  case "$r" in
    docker | compose)
      printf "docker"
      return
      ;;
    binary | systemd)
      printf "binary"
      return
      ;;
    "")
      : ;;
    *)
      T runtime_invalid "$RUNTIME" >&2
      echo >&2
      exit 1
      ;;
  esac
  if ! docker_available; then
    if [ "$cur" = "docker" ]; then printf "docker"; else printf "binary"; fi
    return
  fi
  local ans=""
  if [ -t 0 ]; then
    if [ -n "$cur" ]; then T ask_runtime_cur "$cur"; else T ask_runtime; fi
    read -r ans 2>/dev/null || ans=""
  elif [ -t 1 ] && [ -e /dev/tty ]; then
    if [ -n "$cur" ]; then T ask_runtime_cur "$cur" >/dev/tty || true; else T ask_runtime >/dev/tty || true; fi
    read -r ans </dev/tty 2>/dev/null || ans=""
  fi
  [ -n "$ans" ] || ans="$cur"
  case "$ans" in
    2 | [Dd]ocker | [Cc]ompose)
      printf "docker"
      return
      ;;
  esac
  printf "binary"
}

# 写 compose 文件 (端口与容器内监听端口取 env 文件同一值, 双端 baked 一致)
# SSL_DOMAIN 非空时追加 80/443 映射 (ACME 挑战与 HTTPS) 与证书持久化卷.
write_compose() {
  local imgtag="${1#v}" port="$2"
  mkdir -p "$COMPOSE_DIR"
  {
    cat <<EOF
# go-vless Docker Compose (install.sh 生成)
# 改端口: 同改本文件 ports 映射与 $ENV_FILE 里 PORT (容器内监听端口), 然后 up -d
services:
  go-vless:
    image: $IMAGE:$imgtag
    container_name: go-vless
    restart: unless-stopped
    ports:
      - "$port:$port"
EOF
    if [ -n "${SSL_DOMAIN:-}" ]; then
      cat <<EOF
      - "80:80"
      - "443:443"
EOF
    fi
    cat <<EOF
    env_file:
      - $ENV_FILE
EOF
    if [ -n "${SSL_DOMAIN:-}" ]; then
      cat <<EOF
    volumes:
      - go-vless-certs:/app/cert-cache
volumes:
  go-vless-certs:
EOF
    fi
  } >"$COMPOSE_DIR/docker-compose.yml"
  T compose_written "$COMPOSE_DIR/docker-compose.yml" "$IMAGE:$imgtag"
  echo
}

compose_up() {
  local compose
  compose="$(compose_bin)" || {
    T compose_missing >&2
    echo >&2
    exit 1
  }
  ensure_docker_boot
  # shellcheck disable=SC2086
  (cd "$COMPOSE_DIR" && $compose up -d)
}

# Docker 宿主机开机自启 (daemon 不随开机启动的话, 重启后容器不会回来).
# need_root 已保证 systemctl 存在；失败不中断安装.
ensure_docker_boot() {
  if systemctl enable docker containerd >/dev/null 2>&1; then
    T docker_boot
    echo
  fi
}

compose_down() {
  [ -f "$COMPOSE_DIR/docker-compose.yml" ] || return 0
  local compose
  compose="$(compose_bin 2>/dev/null)" || {
    T compose_skip "$COMPOSE_DIR"
    echo
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
# 隧道开关: $1 默认值 (1 开 / 0 关), 为空默认开.
# 显式 TUNNEL 环境变量由调用方优先处理, 这里只管"问";
# 可交互则询问 (空=取默认), 拿不到终端直接取默认.
ask_tunnel() {
  local def="${1:-1}" ans=""
  if [ -t 0 ]; then
    T ask_tunnel
    read -r ans 2>/dev/null || ans=""
  elif [ -t 1 ] && [ -e /dev/tty ]; then
    T ask_tunnel >/dev/tty || true
    read -r ans </dev/tty 2>/dev/null || ans=""
  fi
  [ -n "$ans" ] || ans="$def"
  case "$ans" in
    [Nn] | [Nn][Oo] | 0 | [Ff]*) printf "0" ;;
    *) printf "1" ;;
  esac
}

# 写配置 (已存在则保留 UUID/密钥等, 但运行模式与隧道开关可重选).
write_env() {
  if [ -f "$ENV_FILE" ]; then
    T env_kept "$ENV_FILE"
    echo
    local explicit_tunnel="${TUNNEL:-}"
    # shellcheck disable=SC1090
    . "$ENV_FILE" 2>/dev/null || true
    if [ -n "$explicit_tunnel" ]; then
      upsert_env TUNNEL "$explicit_tunnel" "$ENV_FILE"
    else
      upsert_env TUNNEL "$(ask_tunnel "${TUNNEL:-1}")" "$ENV_FILE"
    fi
    return
  fi
  local tunnel
  if [ -n "${TUNNEL:-}" ]; then
    tunnel="$TUNNEL"
  else
    tunnel="$(ask_tunnel 1)"
  fi
  cat >"$ENV_FILE" <<EOF
# go-vless 配置 (install.sh 生成, 手改后 systemctl restart go-vless 生效)
UUID=${UUID:-$(cat /proc/sys/kernel/random/uuid)}
PORT=${PORT:-8080}
TUNNEL=$tunnel
TUNNEL_PROTO=${TUNNEL_PROTO:-auto}
MAX_CONN=${MAX_CONN:-4096}
CONFIG_KEY=${CONFIG_KEY:-$(rand_hex)}
DOMAIN=${DOMAIN:-}
SSL_DOMAIN=${SSL_DOMAIN:-}
REGISTER_URL=${REGISTER_URL:-}
REGISTER_NODE_ID=${REGISTER_NODE_ID:-}
EOF
  chmod 600 "$ENV_FILE"
  T env_written "$ENV_FILE"
  echo
}

# 已有配置文件 + 显式三元组: 覆盖写入配对三行, 其余保留.
upsert_env() {  local k="$1" v="$2" f="$3" esc
  esc="$(printf "%s" "$v" | sed 's/[&\\]/\\&/g')"
  if grep -q "^${k}=" "$f" 2>/dev/null; then
    sed -i "s|^${k}=.*|${k}=${esc}|" "$f"
  else
    printf "%s=%s\n" "$k" "$v" >>"$f"
  fi
}

# 重装确认: 二进制/service/compose 任一存在即视为已安装.
# 可交互 (stdin TTY，管道安装时走 /dev/tty) 则询问，默认 Y；
# 拿不到终端时明示后自动继续 (脚本化重装不卡死).
confirm_reinstall() {
  local found=""
  [ -x "$BIN" ] && found="binary"
  [ -f "$UNIT" ] && found="${found:+$found, }systemd"
  [ -f "$COMPOSE_DIR/docker-compose.yml" ] && found="${found:+$found, }docker"
  [ -n "$found" ] || return 0
  local ans=""
  if [ -t 0 ]; then
    T confirm_reinstall "$found"
    read -r ans 2>/dev/null || ans=""
  elif [ -t 1 ] && [ -r /dev/tty ] && [ -w /dev/tty ]; then
    T confirm_reinstall "$found" >/dev/tty || true
    read -r ans </dev/tty 2>/dev/null || ans=""
  else
    T auto_proceed "$found"
    echo
    return 0
  fi
  if reinstall_denied "$ans"; then
    T reinstall_abort >&2
    echo >&2
    exit 0
  fi
}

# 重装问答判定 (供测试与 confirm_reinstall 共用): 空=默认 Y, 显式 no 拒绝.
reinstall_denied() {
  case "$1" in
    [Nn] | [Nn][Oo] | 0 | [Ff]*) return 0 ;;
    *) return 1 ;;
  esac
}

# 安装脚本自身存档 (管道安装时 stdin 无文件, 则从 GitHub 重拉一份).
# 失败只警告不中断 (need_root 已保证目录可写, 一般不会走到).
save_installer() {
  local src="${BASH_SOURCE[0]:-}"
  if [ -n "$src" ] && [ -f "$src" ]; then
    install -m 0755 "$src" "$INSTALLER_DEST" 2>/dev/null || {
      T installer_save_fail "$INSTALLER_DEST" >&2
      echo >&2
      return 0
    }
  else
    curl -fsSL -o "$INSTALLER_DEST" "$INSTALLER_URL" 2>/dev/null || {
      T installer_save_fail "$INSTALLER_URL" >&2
      echo >&2
      return 0
    }
    chmod 0755 "$INSTALLER_DEST" 2>/dev/null || true
  fi
  T installer_saved "$INSTALLER_DEST"
  echo
}

# 老配置迁移: 只补缺失的键 (已有值永不覆盖).
# 新版本加 env 时在这里加一行, 否则 update/重装的老用户永远用不上.
migrate_env() {
  [ -f "$ENV_FILE" ] || return 0
  if ! grep -q "^SSL_DOMAIN=" "$ENV_FILE" 2>/dev/null; then
    printf "SSL_DOMAIN=%s\n" "${SSL_DOMAIN:-}" >>"$ENV_FILE"
  fi
}

apply_register() {
  [ -n "${REGISTER:-}" ] || return 0
  [ -f "$ENV_FILE" ] || {
    T env_missing "$ENV_FILE" >&2
    echo >&2
    exit 1
  }
  upsert_env CONFIG_KEY "$CONFIG_KEY" "$ENV_FILE"
  upsert_env REGISTER_URL "$REGISTER_URL" "$ENV_FILE"
  upsert_env REGISTER_NODE_ID "$REGISTER_NODE_ID" "$ENV_FILE"
  T reg_updated "$REGISTER_URL"
  echo
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
  T mode "$mode"
  echo
  T info_uuid "${UUID:-}"
  echo
  T info_key "${CONFIG_KEY:-}"
  echo
  if [ -n "${REGISTER_URL:-}" ]; then
    T info_dash "$REGISTER_URL"
  else
    T info_dash "$(T dash_none)"
  fi
  echo
  T info_node "${REGISTER_NODE_ID:-}"
  echo
  if [ -n "${SSL_DOMAIN:-}" ]; then
    T info_ssl "$SSL_DOMAIN"
  else
    T info_ssl "$(T ssl_off)"
  fi
  echo
  if [ "${TUNNEL:-1}" = "1" ]; then
    T info_tunnel "$(T tun_on)"
  else
    T info_tunnel "$(T tun_off)"
  fi
  echo
  T info_admin "${PORT:-8080}" "${CONFIG_KEY:-}"
  echo
  if [ "$mode" = "docker" ]; then
    T info_ctl "cd $COMPOSE_DIR && docker compose [up -d|down|restart|ps]"
    echo
    T info_logs "docker logs -f go-vless"
    echo
  else
    T info_ctl "systemctl [start|stop|restart|status] go-vless"
    echo
    T info_logs "journalctl -u go-vless -f"
    echo
  fi
}

do_install() {
  need_root
  confirm_reinstall
  local ver="$VERSION"
  if [ "$ver" = "latest" ]; then
    ver="$(latest_tag)"
  fi
  local mode
  mode="$(ask_runtime "$(current_mode)")"
  T running_mode "$mode"
  echo
  write_env
  migrate_env
  apply_register
  save_installer
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
  migrate_env
  apply_register
  save_installer
  local ver="$VERSION"
  if [ "$ver" = "latest" ]; then
    ver="$(latest_tag)"
  fi
  if [ -f "$COMPOSE_DIR/docker-compose.yml" ]; then
    [ -f "$ENV_FILE" ] || {
      T env_lost "$ENV_FILE" >&2
      echo >&2
      exit 1
    }
    # shellcheck disable=SC1090
    . "$ENV_FILE"
    write_compose "$ver" "${PORT:-8080}"
    local compose
    compose="$(compose_bin)" || {
      T compose_missing >&2
      echo >&2
      exit 1
    }
    ensure_docker_boot
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
    T not_installed >&2
    echo >&2
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
  T uninstalled "$ENV_FILE"
  echo
}

# 直接执行、被 source 仅加载函数、管道安装 (stdin 非文件时 BASH_SOURCE 为空)
# 三种情况都要覆盖：为空视作直接执行，否则管道安装会静默跳过主流程。
if [ -z "${BASH_SOURCE[0]:-}" ] || [ "${BASH_SOURCE[0]}" = "$0" ]; then
  case "$ACTION" in
    install) do_install ;;
    update) do_update ;;
    uninstall) do_uninstall ;;
    *)
      T usage "$0" >&2
      echo >&2
      exit 1
      ;;
  esac
fi
