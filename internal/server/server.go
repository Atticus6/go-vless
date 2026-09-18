// Package server 负责 HTTP 路由装配、服务启动与优雅关闭.
package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	st := &status.Provider{
		Users:         users,
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

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           NewRouter(v, st),
		ReadHeaderTimeout: 10 * time.Second, // 防 Slowloris, hijack 后不再生效
		ReadTimeout:       readTimeout,
		WriteTimeout:      readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	// 优雅关闭
	go gracefulShutdown(srv, tun, cancel)

	log.Printf(i18n.T("server.listening"), cfg.Port)
	if os.Getenv("CONFIG_KEY") == "" {
		log.Println(i18n.T("server.configkey_off"))
	}

	// 反向注册: 带三元组安装时上报 dashboard, 否则仅本地运行.
	// 成功后服务端下发的节点用户 token 按 UUID 同步到 users，即刻生效.
	register.MaybeStart(ctx, cfg.DashboardURL, cfg.NodeID, register.BackendInfo{
		URLs: status.AccessURLs(),
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
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf(i18n.T("server.error"), err)
	}
	log.Println(i18n.T("server.stopped"))
}

func gracefulShutdown(srv *http.Server, tun *tunnel.Tunnel, cancel context.CancelFunc) {
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

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf(i18n.T("server.shutdown_err"), err)
	}
}
