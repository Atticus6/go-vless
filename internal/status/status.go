// Package status 提供 /config 状态信息页: 隧道开关、访问地址、全用户流量、构建信息与运行时状态.
// 鉴权靠 CONFIG_KEY 环境变量 (query ?key= 或 X-Config-Key 头);
// CONFIG_KEY 未设置或密钥不对一律 404.
package status

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/atticus6/go-vless/internal/user"
	"github.com/google/uuid"
)

// 构建期注入: -ldflags "-X github.com/atticus6/go-vless/internal/status.buildTime=2026-...";
// 未注入时回退 debug.ReadBuildInfo 的 vcs.time, 都没有则为 "unknown".
var buildTime = "unknown"

// Provider 状态页依赖. Users 用于路径鉴权 + 取请求者流量.
type Provider struct {
	Users         *user.Registry
	TunnelEnabled bool
	TunnelURL     func() string
	IPv4Supported func() bool
	IPv6Supported func() bool
	StartedAt     time.Time

	egressV4 atomic.Value // egressCache, 出口 IPv4 (按需刷新)
	egressV6 atomic.Value // egressCache, 出口 IPv6 (按需刷新)
}

// Handler 管理员信息页 (鉴权由 WithConfigKey 中间件完成).
// tunnel=true 时 url 为 Argo 隧道地址 (刚启动还在建连时会为空, 刷新重试);
// tunnel=false 时 (如 Vercel) url 取 VERCEL_URL 拼出的 https 地址.
func (p *Provider) Handler(w http.ResponseWriter, r *http.Request) {
	p.refreshEgress()

	url := ""
	if p.TunnelEnabled && p.TunnelURL != nil {
		url = p.TunnelURL()
	}
	if url == "" {
		if host := strings.TrimSpace(os.Getenv("VERCEL_URL")); host != "" {
			host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
			url = "https://" + host
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tunnel":     p.TunnelEnabled,
		"url":        url,
		"ipv4":       boolOr(p.IPv4Supported, true),
		"ipv6":       boolOr(p.IPv6Supported, true),
		"egressIPv4": p.egressIP(false),
		"egressIPv6": p.egressIP(true),
		"users":      formatTraffic(p.userTraffic()),
		"buildTime":  getBuildTime(),
		"binarySize": getBinarySize(),
		"memory":     getMemUsage(),
		"uptime":     time.Since(p.StartedAt).Truncate(time.Second).String(),
	})
}

// WithConfigKey 通信密钥鉴权中间件 (env: CONFIG_KEY).
// 未设置或对不上直接 404, 不泄露接口存在.
func WithConfigKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkConfigKey(r) {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

// AddUsersHandler 运行时批量新增用户: POST /config/users/add,
// JSON {"uuids":["uuid",...]}. UUID 非法则 400 且不生效; 成功返回全量列表.
// 鉴权由 WithConfigKey 中间件完成.
func (p *Provider) AddUsersHandler(w http.ResponseWriter, r *http.Request) {
	ids, ok := p.parseUUIDBody(w, r)
	if !ok {
		return
	}
	for _, id := range ids {
		p.Users.Add(id)
	}
	p.writeUsers(w)
}

// RemoveUsersHandler 运行时批量删除用户: POST /config/users/remove,
// JSON {"uuids":["uuid",...]}. 不存在视为幂等成功; 成功返回全量列表.
// 鉴权由 WithConfigKey 中间件完成.
func (p *Provider) RemoveUsersHandler(w http.ResponseWriter, r *http.Request) {
	ids, ok := p.parseUUIDBody(w, r)
	if !ok {
		return
	}
	for _, id := range ids {
		p.Users.Remove(id)
	}
	p.writeUsers(w)
}

// parseUUIDBody 解析 {"uuids":[...]} 请求体, 失败时直接写 400/500 并返回 ok=false.
func (p *Provider) parseUUIDBody(w http.ResponseWriter, r *http.Request) ([]uuid.UUID, bool) {
	if p.Users == nil {
		http.Error(w, "user registry unavailable", http.StatusInternalServerError)
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		UUIDs []string `json:"uuids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	ids, err := parseUUIDList(req.UUIDs)
	if err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return ids, true
}

// writeUsers 返回 {"ok":true,"users":[...]} 全量列表.
func (p *Provider) writeUsers(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	ids := p.Users.List()
	strs := make([]string, 0, len(ids))
	for _, id := range ids {
		strs = append(strs, id.String())
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "users": strs})
}

func parseUUIDList(ss []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(ss))
	for _, s := range ss {
		id, err := uuid.Parse(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("invalid UUID %q", s)
		}
		out = append(out, id)
	}
	return out, nil
}

// checkConfigKey 校验通信密钥 (env: CONFIG_KEY).
// 未设置或对不上都返回 false.
func checkConfigKey(r *http.Request) bool {
	expected := os.Getenv("CONFIG_KEY")
	if expected == "" {
		return false
	}
	if key := r.URL.Query().Get("key"); key != "" {
		return key == expected
	}
	return r.Header.Get("X-Config-Key") == expected
}

// boolOr 取闭包值, 闭包为 nil 时回退默认值 (乐观默认支持)
func boolOr(fn func() bool, def bool) bool {
	if fn == nil {
		return def
	}
	return fn()
}

// userTraffic 全用户流量快照 (Users 为 nil 时返回空表).
func (p *Provider) userTraffic() map[string]user.UserTraffic {
	if p.Users == nil {
		return map[string]user.UserTraffic{}
	}
	return p.Users.SnapshotAll()
}

// formatTraffic 流量字节数格式化为人类可读 (up/down 均为 "x.xx MB" 风格).
func formatTraffic(all map[string]user.UserTraffic) map[string]map[string]string {
	out := make(map[string]map[string]string, len(all))
	for id, t := range all {
		out[id] = map[string]string{"up": formatBytes(t.Up), "down": formatBytes(t.Down)}
	}
	return out
}

// getBuildTime 构建时间: ldflags 注入 > vcs.time > unknown
func getBuildTime() string {
	if buildTime != "" && buildTime != "unknown" {
		return buildTime
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.time" && s.Value != "" {
				return s.Value
			}
		}
	}
	return "unknown"
}

// getBinarySize 自身二进制文件大小, 格式化为 "6.10 MB" (两位小数); 失败返回 "unknown"
func getBinarySize() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	st, err := os.Stat(exe)
	if err != nil {
		return "unknown"
	}
	return formatMB(uint64(st.Size()))
}

// getMemUsage 内存占用, 均为 "x.xx MB":
// alloc=堆存活对象, sys=向系统申请的虚拟内存, rss=实际占用的物理内存.
// alloc <= rss <= sys 一般成立; 看 OOM / 配额以 rss 为准.
func getMemUsage() map[string]string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return map[string]string{
		"alloc": formatMB(m.Alloc),
		"sys":   formatMB(m.Sys),
		"rss":   formatMB(getRSS(&m)),
	}
}

// getRSS 常驻物理内存: Linux 读 /proc 精确值, 其他平台用 Sys-HeapReleased 估算
func getRSS(m *runtime.MemStats) uint64 {
	if runtime.GOOS == "linux" {
		if rss, err := readProcRSS(); err == nil {
			return rss
		}
	}
	if m.Sys > m.HeapReleased {
		return m.Sys - m.HeapReleased
	}
	return m.Sys
}

// readProcRSS 解析 /proc/self/stat 第 24 个字段 (常驻页数) x 页大小.
// comm 字段可能含空格/括号, 故从最后一个 ')' 往后切分 (之后第 1 个为 field 3).
func readProcRSS() (uint64, error) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, err
	}
	s := string(data)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return 0, fmt.Errorf("malformed /proc/self/stat")
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) < 22 {
		return 0, fmt.Errorf("malformed /proc/self/stat")
	}
	pages, err := strconv.ParseUint(fields[21], 10, 64)
	if err != nil {
		return 0, err
	}
	return pages * uint64(os.Getpagesize()), nil
}

// formatMB 字节数格式化为 "x.xx MB" (两位小数)
func formatMB(n uint64) string {
	return fmt.Sprintf("%.2f MB", float64(n)/1024/1024)
}

// formatBytes 字节数按合适单位格式化 (B/KB/MB/GB/TB, 两位小数), 流量展示用
func formatBytes(n uint64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	f := float64(n) / 1024
	for _, u := range units {
		if f < 1024 || u == "TB" {
			return fmt.Sprintf("%.2f %s", f, u)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.2f TB", f)
}
