// Package server 负责 HTTP 路由装配、服务启动与优雅关闭.
package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/atticus6/go-vless/internal/config"
	"github.com/atticus6/go-vless/internal/i18n"
	"github.com/atticus6/go-vless/internal/register"
	"github.com/atticus6/go-vless/internal/status"
	"github.com/atticus6/go-vless/internal/user"
	"github.com/atticus6/go-vless/internal/vless"
	"github.com/atticus6/go-vless/tunnel"
)

// HTTP 服务调优常量
const (
	readTimeout     = 30 * time.Second // 仅作用于 /health 等普通 HTTP
	idleTimeout     = 120 * time.Second
	maxHeaderBytes  = 4 * 1024
	shutdownTimeout = 10 * time.Second
)

// NewRouter 装配路由: / 走 VLESS, /health 健康检查,
// /config 管理信息页与 /config/users/add|remove 用户管理 (WithConfigKey 鉴权).
func NewRouter(v *vless.Server, st *status.Provider) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", v.Handler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("/config", status.WithConfigKey(st.Handler))
	mux.HandleFunc("/config/", status.WithConfigKey(st.Handler))
	mux.HandleFunc("POST /config/users/add", status.WithConfigKey(st.AddUsersHandler))
	mux.HandleFunc("POST /config/users/add/", status.WithConfigKey(st.AddUsersHandler))
	mux.HandleFunc("POST /config/users/remove", status.WithConfigKey(st.RemoveUsersHandler))
	mux.HandleFunc("POST /config/users/remove/", status.WithConfigKey(st.RemoveUsersHandler))
	mux.HandleFunc("POST /config/update", status.WithConfigKey(st.UpdateHandler))
	mux.HandleFunc("POST /config/update/", status.WithConfigKey(st.UpdateHandler))
	return mux
}

// Run 启动隧道 (按配置) + HTTP 服务, 收到 SIGINT/SIGTERM 时优雅关闭.
func Run(cfg *config.Config) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动 Argo 隧道
	var tun *tunnel.Tunnel
	if cfg.EnableTunnel {
		tun = tunnel.New(cfg.Port, cfg.TunnelProto)
		go func() {
			if err := tun.Start(ctx); err != nil {
				log.Printf(i18n.T("server.tunnel_fail"), err)
			}
		}()
	}

	users := user.New(cfg.Users)
	log.Printf(i18n.T("server.users_loaded"), len(cfg.Users))
	for _, id := range cfg.Users {
		log.Printf("UUID: %s", id.String())
	}

	v := vless.New(users, cfg.MaxConns)
	st := &status.Provider{		Users:         users,
		TunnelEnabled: cfg.EnableTunnel,
		TunnelURL: func() string {
			if tun == nil {
				return ""
			}
			return tun.GetURL()
		},
		IPv4Supported: v.HasIPv4,
		IPv6Supported: v.HasIPv6,
		StartedAt:     time.Now(),
	}

	// 启动摘要: 关键配置一次打全 (密钥只显示是否设置, 不打值).
	logSummary(cfg, v)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           NewRouter(v, st),
		ReadHeaderTimeout: 10 * time.Second, // 防 Slowloris, hijack 后不再生效
		ReadTimeout:       readTimeout,
		WriteTimeout:      readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	mux := srv.Handler

	// 自动 HTTPS: 完整 Linux + 配域名 + 443/80 可用才启用, 否则回退 plain HTTP.
	// 证书首次握手时按需申请 (HTTP-01, 需域名解析到本机且 80 公网可达), 落盘自动续期.
	if domain := tlsDomain(cfg); domain != "" {
		if tlsSrv, httpSrv, certExpiryFn, ok := tryAutoTLS(mux, cfg); ok {
			// TLS 已生效：SSL 域名与证书到期查询进入对外状态（/config urls/tls 段同源）.
			st.SSLDomain = domain
			st.TLSCertExpiry = certExpiryFn
			// PORT 保留明文监听 (隧道回源与存量端口配置不受影响),
			// 443 对外 HTTPS, 80 只做 ACME 挑战.
			servers := []*http.Server{tlsSrv, httpSrv}
			if keepPlainPort(cfg.Port) {
				go func() {
					if err := srv.ListenAndServe(); err != http.ErrServerClosed {
						log.Printf(i18n.T("server.error"), err)
					}
				}()
				servers = append(servers, srv)
				log.Printf(i18n.T("server.listening"), cfg.Port)
			}
			go gracefulShutdown(tun, cancel, cfg.DashboardURL, cfg.NodeID, users, servers...)
			log.Printf(i18n.T("server.tls_on"), domain, certCacheDir())
			afterStart(ctx, cfg, tun, users, st)
			if err := tlsSrv.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
				log.Fatalf(i18n.T("server.error"), err)
			}
			log.Println(i18n.T("server.stopped"))
			return
		}
	}

	// 优雅关闭
	go gracefulShutdown(tun, cancel, cfg.DashboardURL, cfg.NodeID, users, srv)

	log.Printf(i18n.T("server.listening"), cfg.Port)
	afterStart(ctx, cfg, tun, users, st)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf(i18n.T("server.error"), err)
	}
	log.Println(i18n.T("server.stopped"))
}

// logSummary 启动摘要: 构建版本、端口/连接数/隧道/证书域名、注册目标、
// 出口网络与管理后台状态. 排查先看这几行.
func logSummary(cfg *config.Config, v *vless.Server) {
	log.Printf(i18n.T("server.build_info"), status.BuildVersion(), runtime.GOOS, runtime.GOARCH)
	tunnel := i18n.T("server.tunnel_off")
	if cfg.EnableTunnel {
		tunnel = cfg.TunnelProto
	}
	domain := cfg.SSLDomain
	if domain == "" {
		domain = i18n.T("server.none")
	}
	log.Printf(i18n.T("server.cfg_info"), cfg.Port, cfg.MaxConns, tunnel, domain)
	if cfg.DashboardURL != "" {
		node := cfg.NodeID
		if node == "" {
			node = i18n.T("server.none")
		}
		log.Printf(i18n.T("server.register_info"), cfg.DashboardURL, node)
	}
	v4, v6 := i18n.T("probe.unavailable"), i18n.T("probe.unavailable")
	if v.HasIPv4() {
		v4 = i18n.T("probe.ok")
	}
	if v.HasIPv6() {
		v6 = i18n.T("probe.ok")
	}
	log.Printf(i18n.T("server.net_info"), v4, v6)
}

// afterStart 启动后公共收尾: 管理后台提示、反向注册与隧道等待日志.
func afterStart(ctx context.Context, cfg *config.Config, tun *tunnel.Tunnel, users *user.Registry, st *status.Provider) {
	if os.Getenv("CONFIG_KEY") == "" {
		log.Println(i18n.T("server.configkey_off"))
	} else {
		log.Println(i18n.T("server.configkey_on"))
	}

	// 反向注册: 带三元组安装时上报 dashboard, 否则仅本地运行.
	// 成功后服务端下发的节点用户 token 按 UUID 同步到 users，即刻生效.
	register.MaybeStart(ctx, cfg.DashboardURL, cfg.NodeID, register.BackendInfo{
		URLs: st.PublicURLs(),
		TunnelURL: func() string {
			if tun == nil {
				return ""
			}
			return tun.GetURL()
		},
		TunnelEnabled: cfg.EnableTunnel,
		Users:         users,
	})
	if cfg.EnableTunnel {
		log.Println(i18n.T("server.tunnel_waiting"))
	}
}

// gracefulShutdown 收信号后优雅关闭: 先停隧道, 再依次关闭所有 HTTP 服务,
// 最后上报一次全用户流量快照 (内存计数随进程退出清零, 关机前落库不断档).
// 流量上报失败只打日志, 不阻塞退出; 未配置三元组 (纯本地运行) 静默跳过.
func gracefulShutdown(tun *tunnel.Tunnel, cancel context.CancelFunc, dashboardURL, nodeID string, users *user.Registry, srvs ...*http.Server) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println(i18n.T("server.shutdown"))
	cancel()

	if tun != nil {
		tun.Stop()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	for _, srv := range srvs {
		if srv == nil {
			continue
		}
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf(i18n.T("server.shutdown_err"), err)
		}
	}

	// 关机前最终上报：监听已停，不会再有新流量，计数基本定格；
	// 出站网络此时仍可用（停的是本机监听），失败也不阻塞退出.
	key := os.Getenv("CONFIG_KEY")
	if dashboardURL == "" || nodeID == "" || key == "" {
		return
	}
	endpoint := strings.TrimSuffix(dashboardURL, "/") + "/api/traffic/report"
	register.ReportTraffic(endpoint, nodeID, key, users)
}
