package server

import (
	"net"
	"testing"

	"github.com/atticus6/go-vless/internal/config"
)

func configForTest(domain string) *config.Config {
	return &config.Config{SSLDomain: domain}
}

// 证书目录: 默认 cert-cache, SSL_CACHE_DIR 可覆盖.
func TestCertCacheDir(t *testing.T) {
	t.Setenv("SSL_CACHE_DIR", "")
	if got := certCacheDir(); got != "cert-cache" {
		t.Errorf("default = %q, want cert-cache", got)
	}
	t.Setenv("SSL_CACHE_DIR", "/data/certs")
	if got := certCacheDir(); got != "/data/certs" {
		t.Errorf("override = %q, want /data/certs", got)
	}
}

// 端口预检: 被占用的报 err, 空闲的放行.
func TestProbePort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback: %v", err)
	}
	defer ln.Close()
	if err := probePort(ln.Addr().String()); err == nil {
		t.Errorf("occupied %s should fail probe", ln.Addr())
	}
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback: %v", err)
	}
	addr := free.Addr().String()
	_ = free.Close()
	if err := probePort(addr); err != nil {
		t.Errorf("free %s should pass probe: %v", addr, err)
	}
}

// serverless 下即使配了域名也跳过 HTTPS.
func TestTLSDomainSkippedOnVercel(t *testing.T) {
	t.Setenv("VERCEL", "1")
	cfg := configForTest("example.com")
	if got := tlsDomain(cfg); got != "" {
		t.Errorf("vercel should skip TLS, got %q", got)
	}
	t.Setenv("VERCEL", "")
	if got := tlsDomain(cfg); got != "example.com" {
		t.Errorf("normal env should enable TLS, got %q", got)
	}
	cfg = configForTest("")
	if got := tlsDomain(cfg); got != "" {
		t.Errorf("empty domain should disable TLS, got %q", got)
	}
}

// PORT 为 443/80 时不重复监听, 其余保留明文口 (隧道回源用).
func TestKeepPlainPort(t *testing.T) {
	for _, port := range []int{8080, 81, 1, 65535} {
		if !keepPlainPort(port) {
			t.Errorf("port %d should keep plain listener", port)
		}
	}
	for _, port := range []int{80, 443} {
		if keepPlainPort(port) {
			t.Errorf("port %d should not listen twice", port)
		}
	}
}
