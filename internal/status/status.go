// Package status 提供 /config 状态信息页: 隧道开关、访问地址、全用户流量、构建信息与运行时状态.
// 鉴权靠 CONFIG_KEY 环境变量 (query ?key= 或 X-Config-Key 头);
// CONFIG_KEY 未设置或密钥不对一律 404.
package status

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atticus6/go-vless/internal/i18n"
	"github.com/atticus6/go-vless/internal/user"
	"github.com/google/uuid"
)

// 构建期注入: -ldflags "-X github.com/atticus6/go-vless/internal/status.buildTime=2026-...";
// 未注入时回退 debug.ReadBuildInfo 的 vcs.time, 都没有则为 "unknown".
var buildTime = "unknown"

// Provider 状态页依赖. Users 用于路径鉴权 + 取请求者流量.
// SSLDomain 由 server 在 TLS 成功启用后赋值 (未生效保持空),
// TLSCertExpiry 为证书到期查询（只读缓存，不触发申请），同上.
type Provider struct {
	Users         *user.Registry
	SSLDomain     string
	TLSCertExpiry func() (time.Time, bool)
	TunnelEnabled bool
	TunnelURL     func() string
	IPv4Supported func() bool
	IPv6Supported func() bool
	StartedAt     time.Time

	egressV4 atomic.Value // egressCache, 出口 IPv4 (按需刷新)
	egressV6 atomic.Value // egressCache, 出口 IPv6 (按需刷新)
}

// PublicURLs 对外访问地址（/config 的 urls 与反向注册上报同源）:
// 环境变量地址优先（DOMAIN、VERCEL_URL、NF_HOSTS、RAILWAY_PUBLIC_DOMAIN，
// 均支持逗号分隔多值），其后追加 TLS 生效的 SSL 域名（https，无端口），重复去重.
func (p *Provider) PublicURLs() []string {
	urls := envHosts("DOMAIN", "VERCEL_URL", "NF_HOSTS", "RAILWAY_PUBLIC_DOMAIN")
	if p != nil && p.SSLDomain != "" {
		u := "https://" + p.SSLDomain
		dup := false
		for _, v := range urls {
			if v == u {
				dup = true
				break
			}
		}
		if !dup {
			urls = append(urls, u)
		}
	}
	return urls
}

// BuildVersion 构建版本（反向注册上报用），与状态页 buildTime 同源.
func BuildVersion() string {
	return getBuildTime()
}

// tunnel=true 时 url 为 Argo 隧道地址 (刚启动还在建连时会为空, 刷新重试);
// tunnel=false 时 (如 Vercel) url 取 VERCEL_URL 拼出的 https 地址.
func (p *Provider) Handler(w http.ResponseWriter, r *http.Request) {
	p.refreshEgress()

	urls := []string{}
	tunnelURL := ""
	if p.TunnelEnabled && p.TunnelURL != nil {
		tunnelURL = p.TunnelURL()
	}
	// urls 只收环境变量的地址 + 生效的 SSL 域名, 隧道地址走专属 tunnelURL 字段.
	// 地址来源 (按优先级排序, 均支持逗号分隔多值):
	//   DOMAIN: 自绑定的自定义域名
	//   VERCEL_URL: Vercel 自动注入的部署域名
	//   NF_HOSTS: 额外主机列表 (逗号分隔)
	//   RAILWAY_PUBLIC_DOMAIN: Railway 自动注入的公网域名
	//   SSLDomain: TLS 生效时的证书域名 (https 443, 自动追加)
	urls = append(urls, p.PublicURLs()...)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tunnel":     p.TunnelEnabled,
		"tunnelURL":  tunnelURL,
		"urls":       urls,
		"ipv4":       boolOr(p.IPv4Supported, true),
		"ipv6":       boolOr(p.IPv6Supported, true),
		"egressIPv4": p.egressIP(false),
		"egressIPv6": p.egressIP(true),
		"users":      formatTraffic(p.userTraffic()),
		"register":   registerView(),
		"tls":        p.tlsView(),
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
// 报错文案按请求的 Accept-Language 协商 (中英双语).
func (p *Provider) parseUUIDBody(w http.ResponseWriter, r *http.Request) ([]uuid.UUID, bool) {
	lang := i18n.LangFromAcceptLanguage(r.Header.Get("Accept-Language"))
	if p.Users == nil {
		http.Error(w, i18n.TFor(lang, "status.no_registry"), http.StatusInternalServerError)
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		UUIDs []string `json:"uuids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, i18n.SprintfFor(lang, "status.bad_request", err.Error()), http.StatusBadRequest)
		return nil, false
	}
	ids, err := parseUUIDListFor(lang, req.UUIDs)
	if err != nil {
		http.Error(w, i18n.SprintfFor(lang, "status.bad_request", err.Error()), http.StatusBadRequest)
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
	return parseUUIDListFor(i18n.Get(), ss)
}

// parseUUIDListFor 按指定语言返回非法 UUID 报错.
func parseUUIDListFor(lang string, ss []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(ss))
	for _, s := range ss {
		id, err := uuid.Parse(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("%s", i18n.SprintfFor(lang, "status.invalid_uuid", s))
		}
		out = append(out, id)
	}
	return out, nil
}

// 注册下发的用户 token（所属用户的个人凭证）：内存保存，重启清空，
// 下次注册成功时整体替换. /config 鉴权接受 CONFIG_KEY 或其中任一 token.
type userTokenStore struct {
	sync.RWMutex
	m map[string]struct{}
}

var userTokens = &userTokenStore{m: make(map[string]struct{})}

// RegisterSnapshot 节点端视角的服务端（dashboard）注册/同步状态.
// /config 原样返回，供展示“有没有连上服务端”等信息；
// 不含任何密钥（CONFIG_KEY 永不出现），只读快照，重启清空.
type RegisterSnapshot struct {
	Enabled       bool
	DashboardURL  string
	NodeID        string
	LastReportAt  time.Time
	LastSuccessAt time.Time
	LastError     string
	LastSyncAdded int
	LastSyncRemov int
	SyncedUsers   int
	IntervalSec   int
}

var registerState = struct {
	sync.RWMutex
	snap RegisterSnapshot
}{}

// SetRegisterTarget 记录注册目标（心跳启动时调一次）；未配置则记为禁用.
func SetRegisterTarget(enabled bool, dashboardURL, nodeID string) {
	registerState.Lock()
	defer registerState.Unlock()
	registerState.snap.Enabled = enabled
	registerState.snap.DashboardURL = dashboardURL
	registerState.snap.NodeID = nodeID
}

// SetRegisterSuccess 记录一次成功心跳：清空最近错误，记下对账结果与下发周期.
func SetRegisterSuccess(added, removed, syncedTotal, intervalSec int) {
	registerState.Lock()
	defer registerState.Unlock()
	now := time.Now()
	registerState.snap.LastReportAt = now
	registerState.snap.LastSuccessAt = now
	registerState.snap.LastError = ""
	registerState.snap.LastSyncAdded = added
	registerState.snap.LastSyncRemov = removed
	registerState.snap.SyncedUsers = syncedTotal
	registerState.snap.IntervalSec = intervalSec
}

// SetRegisterFailure 记录一次失败心跳：记下最近错误（截断防爆），同步数保持上次.
func SetRegisterFailure(errMsg string, retrySec int) {
	registerState.Lock()
	defer registerState.Unlock()
	registerState.snap.LastReportAt = time.Now()
	registerState.snap.LastError = errMsg
	registerState.snap.IntervalSec = retrySec
}

// GetRegisterSnapshot 取当前快照（/config 展示与测试用）.
func GetRegisterSnapshot() RegisterSnapshot {
	registerState.RLock()
	defer registerState.RUnlock()
	return registerState.snap
}

// ResetRegisterSnapshot 清空快照（仅测试用）.
func ResetRegisterSnapshot() {
	registerState.Lock()
	registerState.snap = RegisterSnapshot{}
	registerState.Unlock()
}

// tlsView /config 的 tls 段：HTTPS 是否启用、证书域名与到期时间.
// 未启用只给 enabled=false；已启用但缓存尚无证书（等待首次握手），
// expiresAt 为 null；daysLeft 为整天数（已过期为负数）.
func (p *Provider) tlsView() map[string]any {
	enabled := p != nil && p.SSLDomain != ""
	view := map[string]any{"enabled": enabled}
	if !enabled {
		return view
	}
	view["domain"] = p.SSLDomain
	exp, ok := time.Time{}, false
	if p.TLSCertExpiry != nil {
		exp, ok = p.TLSCertExpiry()
	}
	if !ok {
		view["expiresAt"] = nil
		return view
	}
	view["expiresAt"] = exp.UTC().Format(time.RFC3339)
	view["daysLeft"] = int(time.Until(exp).Hours() / 24)
	return view
}

// registerView /config 的 register 段视图：时间转 RFC3339（从未成功/无错误给 null），
// 下次同步倒计时在请求时现算.
func registerView() map[string]any {
	s := GetRegisterSnapshot()
	view := map[string]any{"enabled": s.Enabled}
	if !s.Enabled {
		return view
	}
	view["dashboardUrl"] = s.DashboardURL
	view["nodeId"] = s.NodeID
	if s.LastSuccessAt.IsZero() {
		view["lastSuccessAt"] = nil
	} else {
		view["lastSuccessAt"] = s.LastSuccessAt.UTC().Format(time.RFC3339)
	}
	if s.LastError == "" {
		view["lastError"] = nil
	} else {
		view["lastError"] = s.LastError
	}
	view["lastSyncAdded"] = s.LastSyncAdded
	view["lastSyncRemoved"] = s.LastSyncRemov
	view["syncedUsers"] = s.SyncedUsers
	view["heartbeatIntervalSec"] = s.IntervalSec
	next := 0
	if !s.LastReportAt.IsZero() && s.IntervalSec > 0 {
		if remain := s.IntervalSec - int(time.Since(s.LastReportAt).Seconds()); remain > 0 {
			next = remain
		}
	}
	view["nextSyncInSec"] = next
	return view
}

// SetUserTokens 全量替换已注册的用户 token（注册成功后由注册流程调用）.
func SetUserTokens(tokens []string) {
	next := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		if t != "" {
			next[t] = struct{}{}
		}
	}
	userTokens.Lock()
	userTokens.m = next
	userTokens.Unlock()
}

// checkConfigKey 校验通信密钥 (env: CONFIG_KEY) 或已注册的用户 token.
// CONFIG_KEY 未设置时跳过主密钥校验，只认用户 token；两者都对不上直接 404.
func checkConfigKey(r *http.Request) bool {
	var key string
	if key = r.URL.Query().Get("key"); key == "" {
		key = r.Header.Get("X-Config-Key")
	}
	if key == "" {
		return false
	}
	if expected := os.Getenv("CONFIG_KEY"); expected != "" && key == expected {
		return true
	}
	return userTokens.Has(key)
}

// Has 上报的 key 是否为已注册的用户 token（定长比较）.
func (u *userTokenStore) Has(key string) bool {
	if key == "" {
		return false
	}
	u.RLock()
	defer u.RUnlock()
	for t := range u.m {
		if len(t) == len(key) && subtle.ConstantTimeCompare([]byte(t), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

// envHosts 从多个环境变量收集公网地址 (单个或逗号分隔多值),
// 归一化为 https://host 并去重去空, 顺序即参数顺序.
func envHosts(names ...string) []string {
	out := []string{}
	seen := make(map[string]struct{})
	for _, name := range names {
		for _, part := range strings.Split(os.Getenv(name), ",") {
			host := strings.TrimSpace(part)
			if host == "" {
				continue
			}
			host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
			host = strings.TrimSuffix(host, "/")
			if host == "" {
				continue
			}
			u := "https://" + host
			if _, dup := seen[u]; !dup {
				seen[u] = struct{}{}
				out = append(out, u)
			}
		}
	}
	return out
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
