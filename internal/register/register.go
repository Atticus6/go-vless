// Package register 后端反向注册: 带三元组 (服务端地址:节点id:config_key)
// 安装后, 启动即向 dashboard 上报, 之后按服务端下发的心跳周期
// (默认 30 分钟) 常驻上报, 每次全量同步节点用户 UUID;
// 未配置 (地址/节点 id/密钥任一缺失) 则只启动代理服务, 不注册.
package register

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/atticus6/go-vless/internal/i18n"
	"github.com/atticus6/go-vless/internal/status"
	"github.com/atticus6/go-vless/internal/user"
	"github.com/google/uuid"
)

const (
	// 注册失败后的重试周期.
	retryIntervalSec = 60
	// 服务端未下发心跳周期时的默认全量同步周期：30 分钟.
	defaultSyncIntervalSec = 1800
	// 服务端下发周期的钳制范围：小于 60 秒按 60 秒算，避免打爆服务端；
	// 大于 2 小时按 2 小时算，避免离线感知过慢.
	minSyncIntervalSec = 60
	maxSyncIntervalSec = 7200
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
	// 服务端下发的下次全量同步周期（秒）；缺失/非法时用默认 30 分钟.
	HeartbeatIntervalSec int `json:"heartbeatIntervalSec"`
}

// BackendInfo 后端自报的地址信息. TunnelURL 用函数每次心跳现取
// （隧道重连会换地址），urls 为空或取不到时传空，由服务端按优先级选用.
// Users 为 VLESS 用户注册表：每次心跳服务端下发的 userTokens
// 按 UUID 全量对账进去（非法格式跳过），nil 则只做 /config 鉴权同步.
type BackendInfo struct {
	URLs      []string
	TunnelURL func() string
	// 隧道是否开启：开着但地址还没拿到时加快心跳，直到拿到为止.
	TunnelEnabled bool
	Users         *user.Registry
}

// MaybeStart 配置齐全时起注册/心跳协程, 缺配置直接返回 (只启动代理服务).
// 调用方负责 go 出去, ctx 取消时协程退出.
// 无论是否启用都会刷新 /config 的 register 段快照，供展示连接状态.
func MaybeStart(ctx context.Context, dashboardURL, nodeID string, info BackendInfo) {
	key := os.Getenv("CONFIG_KEY")
	if dashboardURL == "" || nodeID == "" || key == "" {
		status.SetRegisterTarget(false, "", "")
		log.Println(i18n.T("register.skipped"))
		return
	}
	if _, err := uuid.Parse(nodeID); err != nil {
		status.SetRegisterTarget(false, "", "")
		log.Printf(i18n.T("register.bad_node"), err)
		return
	}
	u, err := url.Parse(dashboardURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		status.SetRegisterTarget(false, "", "")
		log.Printf(i18n.T("register.bad_url"), dashboardURL)
		return
	}
	status.SetRegisterTarget(true, dashboardURL, nodeID)
	endpoint := strings.TrimSuffix(dashboardURL, "/") + "/api/nodes/register"
	go loop(ctx, endpoint, nodeID, key, info)
}

func loop(ctx context.Context, endpoint, nodeID, key string, info BackendInfo) {
	// 隧道模式下等地址就绪再首报，保证第一次上报就带上可连地址.
	waitTunnelReady(ctx, info)
	// 常驻心跳：成功后按服务端下发的周期（默认 30 分钟）全量同步 UUID；
	// 失败按 60 秒重试，直到成功或进程退出.
	for {
		_, waitSec := report(endpoint, nodeID, key, info)
		t := time.NewTimer(time.Duration(waitSec) * time.Second)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// effectiveInterval 服务端下发周期换算为下次等待秒数：
// 缺失/非法用默认 30 分钟，并钳制到 [60s, 2h].
func effectiveInterval(v int) int {
	if v <= 0 {
		return defaultSyncIntervalSec
	}
	if v < minSyncIntervalSec {
		return minSyncIntervalSec
	}
	if v > maxSyncIntervalSec {
		return maxSyncIntervalSec
	}
	return v
}

// report 上报一次，返回 (成功与否, 下次等待秒数).
// 成功时全量同步 UUID 并按服务端周期等待；失败按 60 秒重试.
func report(endpoint, nodeID, key string, info BackendInfo) (bool, int) {
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
		return fail("encode: "+err.Error(), err, "register.enc_fail")
	}
	reqCtx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fail("build request: "+err.Error(), err, "register.req_fail")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		// 出站超时/DNS/建连失败都走这里，err 文案（如 context deadline exceeded）
		// 同步记入 /config 的 register.lastError，方便排查哪一段卡住.
		return fail(err.Error(), err, "register.do_fail")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	if res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusUnauthorized {
			status.SetRegisterFailure("unauthorized (401): check node id and key", retryIntervalSec)
			log.Println(i18n.T("register.unauth"))
		} else {
			msg := fmt.Sprintf("http %d: %s", res.StatusCode, truncateErr(string(raw)))
			status.SetRegisterFailure(msg, retryIntervalSec)
			log.Printf(i18n.T("register.bad_code"), res.StatusCode, raw)
		}
		return false, retryIntervalSec
	}
	var out registerResponse
	if err := json.Unmarshal(raw, &out); err != nil || !out.OK {
		msg := "bad response: " + truncateErr(string(raw))
		status.SetRegisterFailure(msg, retryIntervalSec)
		log.Printf(i18n.T("register.bad_resp"), raw)
		return false, retryIntervalSec
	}
	// 每次心跳全量同步：所属用户的 token 进内存供 /config 鉴权
	// （重启清空，下次心跳重建），同时按 UUID 对账到 VLESS 注册表.
	status.SetUserTokens(out.UserTokens)
	added, removed, total := syncUserTokens(info.Users, out.UserTokens)
	waitSec := effectiveInterval(out.HeartbeatIntervalSec)
	status.SetRegisterSuccess(added, removed, total, waitSec)
	log.Println(i18n.T("register.ok"))
	return true, waitSec
}

// fail 记录失败快照并打日志的公共出口：快照记短文案，日志走 i18n 模板.
func fail(msg string, err error, logKey string) (bool, int) {
	status.SetRegisterFailure(truncateErr(msg), retryIntervalSec)
	log.Printf(i18n.T(logKey), err)
	return false, retryIntervalSec
}

// truncateErr 快照错误文案截断到 200 字符，防止超大响应体撑爆快照.
func truncateErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// syncUserTokens 把服务端下发的 token 按 UUID 全量对账到注册表.
// 服务端为全量真相：新增的 Add 进去，服务端已删的从注册表移除.
// 只动曾经由服务端下发过的 UUID，本地 --uuid/UUID 启动用户不受影响
// （除非它恰好也出现在服务端名单里，此时以服务端为准）.
// 非 UUID 格式跳过（仍保留在 /config 鉴权里），nil 注册表只做鉴权同步.
var (
	serverSyncedMu sync.Mutex
	serverSynced   = make(map[uuid.UUID]struct{})
)

func syncUserTokens(reg *user.Registry, tokens []string) (added, removed, total int) {
	want := make(map[uuid.UUID]struct{}, len(tokens))
	for _, t := range tokens {
		id, err := uuid.Parse(t)
		if err != nil {
			continue
		}
		want[id] = struct{}{}
	}
	serverSyncedMu.Lock()
	toAdd := make([]uuid.UUID, 0, len(want))
	for id := range want {
		if _, ok := serverSynced[id]; !ok {
			toAdd = append(toAdd, id)
		}
	}
	toRemove := make([]uuid.UUID, 0)
	for id := range serverSynced {
		if _, ok := want[id]; !ok {
			toRemove = append(toRemove, id)
		}
	}
	serverSynced = want
	total = len(want)
	serverSyncedMu.Unlock()
	if reg == nil {
		return 0, 0, total
	}
	for _, id := range toAdd {
		if !reg.Valid(id) {
			reg.Add(id)
			added++
		} else {
			reg.Add(id)
		}
	}
	for _, id := range toRemove {
		if reg.Remove(id) {
			removed++
		}
	}
	if added > 0 || removed > 0 {
		log.Printf(i18n.T("register.users_synced"), added, removed)
	}
	return added, removed, total
}

// resetSyncState 仅测试用：清空服务端下发快照，隔离各用例.
func resetSyncState() {
	serverSyncedMu.Lock()
	serverSynced = make(map[uuid.UUID]struct{})
	serverSyncedMu.Unlock()
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
