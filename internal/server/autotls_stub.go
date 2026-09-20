//go:build notunnel

package server

import (
	"log"
	"net/http"
	"time"

	"github.com/atticus6/go-vless/internal/config"
	"github.com/atticus6/go-vless/internal/i18n"
)

// tryAutoTLS 在 notunnel 构建下直接回退: serverless 瘦身构建不引入
// autocert 依赖, 配了域名也走 plain HTTP, 与 server.go 的调用保持 API 兼容.
func tryAutoTLS(_ http.Handler, cfg *config.Config) (tlsSrv, httpSrv *http.Server, certExpiryFn func() (time.Time, bool), ok bool) {
	log.Printf(i18n.T("server.tls_nobuild"), cfg.Port)
	return nil, nil, nil, false
}
