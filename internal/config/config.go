package config

import (
	"flag"
	"log"
	"os"
	"strconv"

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
type Config struct {
	Users        []uuid.UUID
	Port         int
	EnableTunnel bool
	TunnelProto  string
	MaxConns     int
}

// Parse 注册 flag 并解析 (含环境变量覆盖默认值), 失败直接 Fatal.
func Parse() *Config {
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
	flag.StringVar(&uuidStr, "uuid", defaultUUIDVal, "VLESS UUID(s), comma-separated for multi-user (env: UUID)")
	flag.IntVar(&port, "port", defaultPortVal, "Server Port (env: PORT)")
	flag.BoolVar(&enableTunnel, "tunnel", enableTunnelVal, "Enable Argo Tunnel (env: TUNNEL)")
	flag.StringVar(&tunnelProto, "tunnel-proto", tunnelProtoVal, "Argo tunnel protocol: auto|quic|http2 (env: TUNNEL_PROTO)")

	maxConnsVal := 4096
	if envMax := os.Getenv("MAX_CONN"); envMax != "" {
		if n, err := strconv.Atoi(envMax); err == nil && n > 0 {
			maxConnsVal = n
		}
	}
	var maxConns int
	flag.IntVar(&maxConns, "max-conn", maxConnsVal, "Max concurrent VLESS sessions (env: MAX_CONN)")

	flag.Parse()

	userIDs, err := user.ParseList(uuidStr)
	if err != nil {
		log.Fatalf("Invalid UUID: %v", err)
	}

	return &Config{
		Users:        userIDs,
		Port:         port,
		EnableTunnel: enableTunnel,
		TunnelProto:  tunnelProto,
		MaxConns:     maxConns,
	}
}
