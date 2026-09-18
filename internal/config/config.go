package config

import (
	"flag"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/atticus6/go-vless/internal/i18n"
	"github.com/atticus6/go-vless/internal/user"
	"github.com/google/uuid"
)

// 默认值
const (
	DefaultUUID = "14725836-1234-5678-9abc-def012345678"
	DefaultPort = 8080
)

// Config 服务配置: flag 优先, 其次环境变量, 最后默认值.
// UUID 支持逗号分隔多用户; Vercel 环境下 EnableTunnel 默认关闭 (TUNNEL=1 可显式覆盖).
// DashboardURL/NodeID 配对出现时启用反向注册 (上报 dashboard), 缺任一项则只启动代理服务.
type Config struct {
	Users        []uuid.UUID
	Port         int
	EnableTunnel bool
	TunnelProto  string
	MaxConns     int
	DashboardURL string
	NodeID       string
}

// Parse 注册 flag 并解析 (含环境变量覆盖默认值), 失败直接 Fatal.
// 提示信息语言: --lang (或 GO_VLESS_LANG / LANG) 支持 auto|zh|en, 默认跟随系统.
func Parse() *Config {
	// --help 先按环境预设语言展示; --lang 在 Parse 后再显式覆盖运行时语言.
	// 预扫描 os.Args 让 `go-vless --lang en --help` 也能按指定语言打印帮助.
	preLang := ""
	for i, a := range os.Args {
		if a == "--lang" && i+1 < len(os.Args) {
			preLang = os.Args[i+1]
		} else if strings.HasPrefix(a, "--lang=") {
			preLang = strings.TrimPrefix(a, "--lang=")
		}
	}
	if preLang != "" && preLang != "auto" {
		i18n.Set(preLang)
	}

	defaultPortVal := DefaultPort
	defaultUUIDVal := DefaultUUID
	enableTunnelVal := true
	tunnelProtoVal := "auto"

	// Vercel (serverless) 下隧道无意义且 cloudflared 会被条件编译裁掉,
	// 检测到 Vercel 环境自动默认关闭, 仍可用 TUNNEL=1 显式覆盖.
	if os.Getenv("VERCEL") != "" {
		enableTunnelVal = false
	}

	// 环境变量覆盖默认值
	if envUUID := os.Getenv("UUID"); envUUID != "" {
		defaultUUIDVal = envUUID
	}
	if envPort := os.Getenv("PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			defaultPortVal = p
		}
	}
	if envTun := os.Getenv("TUNNEL"); envTun != "" {
		if b, err := strconv.ParseBool(envTun); err == nil {
			enableTunnelVal = b
		}
	}
	if envProto := os.Getenv("TUNNEL_PROTO"); envProto != "" {
		tunnelProtoVal = envProto
	}

	var uuidStr string
	var port int
	var enableTunnel bool
	var tunnelProto string
	var langFlag string
	flag.StringVar(&uuidStr, "uuid", defaultUUIDVal, i18n.T("flag.uuid"))
	flag.IntVar(&port, "port", defaultPortVal, i18n.T("flag.port"))
	flag.BoolVar(&enableTunnel, "tunnel", enableTunnelVal, i18n.T("flag.tunnel"))
	flag.StringVar(&tunnelProto, "tunnel-proto", tunnelProtoVal, i18n.T("flag.tunnel_proto"))

	maxConnsVal := 4096
	if envMax := os.Getenv("MAX_CONN"); envMax != "" {
		if n, err := strconv.Atoi(envMax); err == nil && n > 0 {
			maxConnsVal = n
		}
	}
	var maxConns int
	flag.IntVar(&maxConns, "max-conn", maxConnsVal, i18n.T("flag.max_conn"))

	var dashboardURL string
	var nodeID string
	flag.StringVar(&dashboardURL, "dashboard-url", os.Getenv("REGISTER_URL"), i18n.T("flag.dashboard_url"))
	flag.StringVar(&nodeID, "node-id", os.Getenv("REGISTER_NODE_ID"), i18n.T("flag.node_id"))
	flag.StringVar(&langFlag, "lang", "auto", i18n.T("flag.lang"))

	flag.Parse()

	if langFlag != "" && langFlag != "auto" {
		if !isValidLang(langFlag) {
			log.Fatalf(i18n.T("flag.lang_invalid"), langFlag)
		}
		i18n.Set(langFlag)
	}

	userIDs, err := user.ParseList(uuidStr)
	if err != nil {
		log.Fatalf(i18n.T("cfg.invalid_uuid"), err)
	}
	return &Config{
		Users:        userIDs,
		Port:         port,
		EnableTunnel: enableTunnel,
		TunnelProto:  tunnelProto,
		MaxConns:     maxConns,
		DashboardURL: dashboardURL,
		NodeID:       nodeID,
	}
}

// isValidLang 校验 --lang 取值: auto|zh|en (兼容 zh_CN.UTF-8 / en-US 等变体).
func isValidLang(v string) bool {
	l := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(v), "_", "-"))
	return l == "auto" || l == "zh" || strings.HasPrefix(l, "zh-") ||
		l == "en" || strings.HasPrefix(l, "en-")
}
