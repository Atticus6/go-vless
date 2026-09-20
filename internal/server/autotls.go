package server

import (
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/atticus6/go-vless/internal/config"
	"github.com/atticus6/go-vless/internal/i18n"
)

// 自动 HTTPS 常量: 443 对外服务, 80 只给 ACME HTTP-01 挑战用.
const (
	tlsAddr  = ":443"
	acmeAddr = ":80"
	httpTO   = 10 * time.Second // 80 端口读头超时, 防 Slowloris
)

// tlsDomain HTTPS 生效条件: 配了域名且非 serverless 环境
// (Vercel 等绑不了 80/443). 返回 "" 表示走 plain HTTP.
func tlsDomain(cfg *config.Config) string {
	if cfg.SSLDomain == "" {
		return ""
	}
	if os.Getenv("VERCEL") != "" {
		log.Println(i18n.T("server.tls_skipped_serverless"))
		return ""
	}
	return cfg.SSLDomain
}

// certCacheDir 证书缓存目录: SSL_CACHE_DIR 优先, 默认 ./cert-cache.
// 落盘后自动续期无需重新申请 (重建容器前请持久化该目录, 否则有频率限制风险).
func certCacheDir() string {
	if d := strings.TrimSpace(os.Getenv("SSL_CACHE_DIR")); d != "" {
		return d
	}
	return "cert-cache"
}

// newAutocertManager、tlsServers 与 tryAutoTLS 按构建切分，见
// autotls_acme.go (!notunnel, 完整 Linux) 与 autotls_stub.go (notunnel,
// serverless 瘦身构建，不引入 autocert 依赖).

// keepPlainPort TLS 模式下是否保留 PORT 明文监听:
// PORT 本来就是 443/80 时不再重复监听 (已由 TLS/挑战服务接管).
func keepPlainPort(port int) bool {
	return port != 443 && port != 80
}

// probePort 端口可用性预检: 被占用则直接回退 HTTP, 避免半吊子 HTTPS.
func probePort(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ln.Close()
}

// ensureCacheDir 证书目录可写预检: 落不了盘续期就无意义, 直接回退 HTTP.
func ensureCacheDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}
