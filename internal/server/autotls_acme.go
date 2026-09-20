//go:build !notunnel

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/atticus6/go-vless/internal/config"
	"github.com/atticus6/go-vless/internal/i18n"
	"golang.org/x/crypto/acme/autocert"
)

// newAutocertManager 构造 ACME 管理器: 白名单仅放行本域名,
// 联系邮箱走 SSL_EMAIL (可空, 仅用于过期提醒).
func newAutocertManager(domain, cacheDir string) *autocert.Manager {
	return &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domain),
		Cache:      autocert.DirCache(cacheDir),
		Email:      os.Getenv("SSL_EMAIL"),
	}
}

// tlsServers 组装 443 HTTPS 服务与 80 挑战服务 (与 plain 共用一套超时调优).
func tlsServers(mux http.Handler, m *autocert.Manager) (tlsSrv, httpSrv *http.Server) {
	tlsSrv = &http.Server{
		Addr:    tlsAddr,
		Handler: mux,
		TLSConfig: &tls.Config{
			GetCertificate: m.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		},
		ReadHeaderTimeout: 10 * time.Second, // 防 Slowloris, hijack 后不再生效
		ReadTimeout:       readTimeout,
		WriteTimeout:      readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	// 80 只处理 ACME 挑战, 其余一律 301 到 https.
	httpSrv = &http.Server{
		Addr:              acmeAddr,
		Handler:           m.HTTPHandler(nil),
		ReadHeaderTimeout: httpTO,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	return tlsSrv, httpSrv
}

// certExpiry 从证书缓存读域名证书的到期时间.
// 只读缓存、不触发申请：默认 ECDSA 键为纯域名，兼容易 RSA 键（domain+rsa）；
// 无证书/解析失败返回 false（/config 显示"等待首次握手"）.
func certExpiry(cache autocert.Cache, domain string) (time.Time, bool) {
	if cache == nil {
		return time.Time{}, false
	}
	d := strings.ToLower(strings.Trim(strings.TrimSpace(domain), "."))
	if d == "" {
		return time.Time{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, key := range []string{d, d + "+rsa"} {
		data, err := cache.Get(ctx, key)
		if err != nil {
			continue
		}
		crt, err := tls.X509KeyPair(data, data)
		if err != nil || len(crt.Certificate) == 0 {
			continue
		}
		leaf := crt.Leaf
		if leaf == nil {
			leaf, err = x509.ParseCertificate(crt.Certificate[0])
			if err != nil {
				continue
			}
		}
		if leaf.NotAfter.IsZero() {
			continue
		}
		return leaf.NotAfter, true
	}
	return time.Time{}, false
}

// tryAutoTLS 预检 443/80 与证书目录, 通过则组装服务返回 true;
// 任一不过返回 false, 调用方回退 plain HTTP (服务保持可用).
// 同时返回证书到期查询闭包（只读缓存，供 /config 的 tls 段展示）.
func tryAutoTLS(mux http.Handler, cfg *config.Config) (tlsSrv, httpSrv *http.Server, certExpiryFn func() (time.Time, bool), ok bool) {
	cacheDir := certCacheDir()
	if err := ensureCacheDir(cacheDir); err != nil {
		log.Printf(i18n.T("server.tls_unavailable"), cfg.Port, err)
		return nil, nil, nil, false
	}
	if err := probePort(tlsAddr); err != nil {
		log.Printf(i18n.T("server.tls_unavailable"), cfg.Port, err)
		return nil, nil, nil, false
	}
	if err := probePort(acmeAddr); err != nil {
		log.Printf(i18n.T("server.tls_unavailable"), cfg.Port, err)
		return nil, nil, nil, false
	}
	m := newAutocertManager(cfg.SSLDomain, cacheDir)
	tlsSrv, httpSrv = tlsServers(mux, m)
	certExpiryFn = func() (time.Time, bool) {
		return certExpiry(m.Cache, cfg.SSLDomain)
	}
	go func() {
		if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf(i18n.T("server.error"), err)
		}
	}()
	return tlsSrv, httpSrv, certExpiryFn, true
}
