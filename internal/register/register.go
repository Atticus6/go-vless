// Package register 后端反向注册: 带三元组 (服务端地址:节点id:config_key)
// 安装后, 启动即向 dashboard 上报一次, 之后按服务端下发的心跳周期重复上报;
// 未配置 (地址/节点 id/密钥任一缺失) 则只启动代理服务, 不注册.
package register

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/atticus6/go-vless/internal/i18n"
	"github.com/atticus6/go-vless/internal/status"
	"github.com/google/uuid"
)

const (
	// 注册失败后的重试周期；成功一次即停，不再轮询.
	retryIntervalSec = 60
	// 隧道模式下首报前等待地址就绪：轮询间隔与总超时（超时则先报已有数据）。
	tunnelPollInterval = 5 * time.Second
	tunnelWaitTimeout  = 2 * time.Minute
	httpTimeout        = 15 * time.Second
)

type registerRequest struct {
	ID        string   `json:"id"`
	Key       string   `json:"key"`
	Version   string   `json:"version,omitempty"`
	URLs      []string `json:"urls,omitempty"`
	TunnelURL string   `json:"tunnelUrl,omitempty"`
}

type registerResponse struct {
	OK         bool     `json:"ok"`
	UserTokens []string `json:"userTokens"`
}

// BackendInfo 后端自报的地址信息. TunnelURL 用函数每次心跳现取
// （隧道重连会换地址），urls 为空或取不到时传空，由服务端按优先级选用.
type BackendInfo struct {
	URLs      []string
	TunnelURL func() string
	// 隧道是否开启：开着但地址还没拿到时加快心跳，直到拿到为止.
	TunnelEnabled bool
}

// MaybeStart 配置齐全时起注册/心跳协程, 缺配置直接返回 (只启动代理服务).
// 调用方负责 go 出去, ctx 取消时协程退出.
func MaybeStart(ctx context.Context, dashboardURL, nodeID string, info BackendInfo) {
	key := os.Getenv("CONFIG_KEY")
	if dashboardURL == "" || nodeID == "" || key == "" {
		log.Println(i18n.T("register.skipped"))
		return
	}
	if _, err := uuid.Parse(nodeID); err != nil {
		log.Printf(i18n.T("register.bad_node"), err)
		return
	}
	u, err := url.Parse(dashboardURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		log.Printf(i18n.T("register.bad_url"), dashboardURL)
		return
	}
	endpoint := strings.TrimSuffix(dashboardURL, "/") + "/api/nodes/register"
	go loop(ctx, endpoint, nodeID, key, info)
}

func loop(ctx context.Context, endpoint, nodeID, key string, info BackendInfo) {
	// 隧道模式下等地址就绪再首报，保证第一次上报就带上可连地址.
	waitTunnelReady(ctx, info)
	// 成功一次即停；失败才按固定周期重试，直到成功或进程退出.
	if report(endpoint, nodeID, key, info) {
		return
	}
	t := time.NewTicker(time.Duration(retryIntervalSec) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if report(endpoint, nodeID, key, info) {
				return
			}
		}
	}
}

// report 上报一次，成功返回 true.
func report(endpoint, nodeID, key string, info BackendInfo) bool {
	tunnelURL := ""
	if info.TunnelURL != nil {
		tunnelURL = info.TunnelURL()
	}
	payload, err := json.Marshal(registerRequest{
		ID:        nodeID,
		Key:       key,
		Version:   status.BuildVersion(),
		URLs:      info.URLs,
		TunnelURL: tunnelURL,
	})
	if err != nil {
		log.Printf(i18n.T("register.enc_fail"), err)
		return false
	}
	reqCtx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		log.Printf(i18n.T("register.req_fail"), err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf(i18n.T("register.do_fail"), err)
		return false
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	if res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusUnauthorized {
			log.Println(i18n.T("register.unauth"))
		} else {
			log.Printf(i18n.T("register.bad_code"), res.StatusCode, raw)
		}
		return false
	}
	var out registerResponse
	if err := json.Unmarshal(raw, &out); err != nil || !out.OK {
		log.Printf(i18n.T("register.bad_resp"), raw)
		return false
	}
	// 注册成功即停；所属用户的 token 进内存，供 /config 鉴权（重启清空，下次注册重建）.
	status.SetUserTokens(out.UserTokens)
	log.Println(i18n.T("register.ok"))
	return true
}

// waitTunnelReady 隧道开启时等待地址就绪再返回；未开启、已就绪、
// 超时或 ctx 退出时直接返回（从不阻塞代理启动，只影响首报时机）.
func waitTunnelReady(ctx context.Context, info BackendInfo) {
	waitTunnelReadyWithTimeout(ctx, info, tunnelPollInterval, tunnelWaitTimeout)
}

func waitTunnelReadyWithTimeout(
	ctx context.Context,
	info BackendInfo,
	pollInterval, timeout time.Duration,
) {
	if !info.TunnelEnabled || info.TunnelURL == nil {
		return
	}
	if info.TunnelURL() != "" {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
			if info.TunnelURL() != "" {
				return
			}
		}
	}
}
