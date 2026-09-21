package register

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/atticus6/go-vless/internal/user"
	"github.com/google/uuid"
)

// 注册 wire 格式：POST {dashboard}/api/nodes/register，JSON {id,key,version}，
// 成功回 {"ok":true}，并按服务端下发周期等待下次心跳.
func TestReportSuccess(t *testing.T) {
	var gotMethod, gotPath, gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotCT = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"userTokens":["tok-1"],"heartbeatIntervalSec":1800}`)
	}))
	defer srv.Close()

	ok, waitSec := report(
		srv.URL+"/api/nodes/register",
		"123e4567-e89b-12d3-a456-426614174000",
		"testkey",
		BackendInfo{
			URLs:      []string{"https://example.com"},
			TunnelURL: func() string { return "https://tunnel.example.com" },
		},
	)
	if !ok {
		t.Fatal("report returned not-ok")
	}
	if waitSec != 1800 {
		t.Errorf("waitSec = %d, want 1800 (server-issued)", waitSec)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/nodes/register" {
		t.Errorf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotBody["id"] != "123e4567-e89b-12d3-a456-426614174000" || gotBody["key"] != "testkey" {
		t.Errorf("body = %v", gotBody)
	}
	if _, hasVersion := gotBody["version"]; !hasVersion {
		t.Error("body missing version")
	}
	urls, _ := gotBody["urls"].([]any)
	if len(urls) != 1 || urls[0] != "https://example.com" {
		t.Errorf("body urls = %v", gotBody["urls"])
	}
	if gotBody["tunnelUrl"] != "https://tunnel.example.com" {
		t.Errorf("body tunnelUrl = %v", gotBody["tunnelUrl"])
	}
}

// 注册成功后服务端下发的 userTokens 按 UUID 全量对账到 Registry:
// 新增即刻生效，服务端已删的即刻失效；非法格式跳过，已存在幂等;
// 本地启动用户不受影响.
func TestReportSyncsUserTokens(t *testing.T) {
	resetSyncState()
	want1 := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	want2 := "123e4567-e89b-12d3-a456-426614174000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"userTokens":[%q,%q,"not-a-uuid"]}`, want1, want2)
	}))
	defer srv.Close()

	reg := user.New(nil)
	ok, _ := report(
		srv.URL+"/api/nodes/register",
		"123e4567-e89b-12d3-a456-426614174000",
		"testkey",
		BackendInfo{Users: reg},
	)
	if !ok {
		t.Fatal("report returned not-ok")
	}
	for _, want := range []string{want1, want2} {
		id := uuid.MustParse(want)
		if !reg.Valid(id) {
			t.Errorf("registry missing synced token %s", want)
		}
	}
	if n := len(reg.List()); n != 2 {
		t.Errorf("registry size = %d, want 2 (invalid token skipped)", n)
	}
	// 幂等：再次上报同一批不重复计数、不报错.
	ok2, _ := report(srv.URL+"/api/nodes/register", "123e4567-e89b-12d3-a456-426614174000", "testkey", BackendInfo{Users: reg})
	if !ok2 {
		t.Fatal("second report returned not-ok")
	}
	if n := len(reg.List()); n != 2 {
		t.Errorf("after resync registry size = %d, want 2", n)
	}
}

// 服务端删除节点用户后，下次注册对账时从 Registry 移除；本地启动用户保留.
func TestReportRemovesDeletedTokens(t *testing.T) {
	resetSyncState()
	tokA := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	tokB := "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee"
	local := "cccccccc-cccc-cccc-dddd-eeeeeeeeeeee"
	current := []string{tokA, tokB}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "userTokens": current})
	}))
	defer srv.Close()

	reg := user.New([]uuid.UUID{uuid.MustParse(local)})
	nodeID := "123e4567-e89b-12d3-a456-426614174000"
	ok, _ := report(srv.URL+"/api/nodes/register", nodeID, "k", BackendInfo{Users: reg})
	if !ok {
		t.Fatal("first report not-ok")
	}
	if n := len(reg.List()); n != 3 {
		t.Fatalf("after first sync size = %d, want 3 (local+2)", n)
	}
	// 服务端删掉 tokB.
	current = []string{tokA}
	ok2, _ := report(srv.URL+"/api/nodes/register", nodeID, "k", BackendInfo{Users: reg})
	if !ok2 {
		t.Fatal("second report not-ok")
	}
	if reg.Valid(uuid.MustParse(tokB)) {
		t.Error("deleted server token still valid")
	}
	if !reg.Valid(uuid.MustParse(tokA)) {
		t.Error("remaining server token missing")
	}
	if !reg.Valid(uuid.MustParse(local)) {
		t.Error("local startup user should be preserved")
	}
	if n := len(reg.List()); n != 2 {
		t.Errorf("after removal size = %d, want 2", n)
	}
}

  // 401（key 对不上）返回失败，下次 60 秒后重试.
func TestReportUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
	}))
	defer srv.Close()

	ok, waitSec := report(srv.URL+"/api/nodes/register", "123e4567-e89b-12d3-a456-426614174000", "wrong", BackendInfo{})
	if ok {
		t.Error("report with 401 should return not-ok")
	}
	if waitSec != retryIntervalSec {
		t.Errorf("waitSec = %d, want %d (retry)", waitSec, retryIntervalSec)
	}
}

// 心跳周期：服务端下发值直接采用；缺失用默认 30 分钟；越界钳制到 [60s, 2h].
func TestReportHeartbeatInterval(t *testing.T) {
	resetSyncState()
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"server 30min", `{"ok":true,"heartbeatIntervalSec":1800}`, 1800},
		{"missing defaults 30min", `{"ok":true}`, defaultSyncIntervalSec},
		{"too small clamps to 60s", `{"ok":true,"heartbeatIntervalSec":5}`, minSyncIntervalSec},
		{"too large clamps to 2h", `{"ok":true,"heartbeatIntervalSec":99999}`, maxSyncIntervalSec},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			ok, waitSec := report(srv.URL+"/api/nodes/register", "123e4567-e89b-12d3-a456-426614174000", "k", BackendInfo{})
			if !ok {
				t.Fatal("report returned not-ok")
			}
			if waitSec != tc.want {
				t.Errorf("waitSec = %d, want %d", waitSec, tc.want)
			}
		})
	}
}

// 等待隧道就绪：未开启/已就绪立即返回；建连中等待到拿到地址；
// 超时或取消时直接返回（首报用已有数据，不无限阻塞）.
func TestWaitTunnelReady(t *testing.T) {
	fastPoll, fastTimeout := 10*time.Millisecond, 200*time.Millisecond

	// 未开启隧道：立即返回.
	start := time.Now()
	waitTunnelReadyWithTimeout(
		context.Background(),
		BackendInfo{TunnelEnabled: false, TunnelURL: func() string { return "" }},
		fastPoll, fastTimeout,
	)
	if elapsed := time.Since(start); elapsed >= fastTimeout {
		t.Errorf("disabled tunnel should return immediately, took %v", elapsed)
	}

	// 已有地址：立即返回.
	calls := 0
	waitTunnelReadyWithTimeout(
		context.Background(),
		BackendInfo{TunnelEnabled: true, TunnelURL: func() string {
			calls++
			return "https://tunnel.example.com"
		}},
		fastPoll, fastTimeout,
	)
	if calls != 1 {
		t.Errorf("ready tunnel should be checked once, got %d calls", calls)
	}

	// 建连中：第 3 次轮询拿到地址后返回.
	calls = 0
	waitTunnelReadyWithTimeout(
		context.Background(),
		BackendInfo{TunnelEnabled: true, TunnelURL: func() string {
			calls++
			if calls >= 3 {
				return "https://tunnel.example.com"
			}
			return ""
		}},
		fastPoll, time.Second,
	)
	if calls != 3 {
		t.Errorf("should return once tunnel is ready, got %d calls", calls)
	}

	// 一直拿不到：超时返回.
	start = time.Now()
	waitTunnelReadyWithTimeout(
		context.Background(),
		BackendInfo{TunnelEnabled: true, TunnelURL: func() string { return "" }},
		fastPoll, 100*time.Millisecond,
	)
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("should wait until timeout, returned after %v", elapsed)
	}

	// ctx 取消：立即返回.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	waitTunnelReadyWithTimeout(
		ctx,
		BackendInfo{TunnelEnabled: true, TunnelURL: func() string { return "" }},
		time.Hour, time.Hour,
	)
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("cancelled ctx should return immediately, took %v", elapsed)
	}
}

// 流量上报 wire 格式：POST {dashboard}/api/traffic/report，
// JSON {id,key,users:[{uuid,upBytes,downBytes}]}，成功回 {"ok":true}；
// 注册表为 nil 或空时不发请求.
func TestReportTraffic(t *testing.T) {
	id1 := uuid.New()
	id2 := uuid.New()
	reg := user.New([]uuid.UUID{id1, id2})
	reg.StatsFor(id1).AddUp(200 * 1024)
	reg.StatsFor(id1).AddDown(300 * 1024)
	reg.StatsFor(id2).AddUp(200 * 1024)

	var gotMethod, gotPath string
	var gotBody map[string]any
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"recorded":2}`)
	}))
	defer srv.Close()

	if !ReportTraffic(srv.URL+"/api/traffic/report", "123e4567-e89b-12d3-a456-426614174000", "k", reg) {
		t.Fatal("reportTraffic returned false")
	}
	if calls != 1 {
		t.Fatalf("requests = %d, want 1", calls)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/traffic/report" {
		t.Errorf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotBody["id"] != "123e4567-e89b-12d3-a456-426614174000" || gotBody["key"] != "k" {
		t.Errorf("body id/key = %v", gotBody)
	}
	users, _ := gotBody["users"].([]any)
	if len(users) != 2 {
		t.Fatalf("body users = %v, want 2 entries", gotBody["users"])
	}
	byUUID := map[string]map[string]any{}
	for _, u := range users {
		m, _ := u.(map[string]any)
		uuidStr, _ := m["uuid"].(string)
		byUUID[uuidStr] = m
	}
	e1 := byUUID[id1.String()]
	if e1 == nil || e1["upBytes"] != float64(200*1024) || e1["downBytes"] != float64(300*1024) {
		t.Errorf("user1 entry = %v, want upBytes=204800 downBytes=307200", e1)
	}
	e2 := byUUID[id2.String()]
	if e2 == nil || e2["upBytes"] != float64(200*1024) || e2["downBytes"] != float64(0) {
		t.Errorf("user2 entry = %v, want upBytes=204800 downBytes=0", e2)
	}
	// 上报成功后计数清零（用户保留），下个周期重新累计.
	if up, down := reg.StatsFor(id1).Snapshot(); up != 0 || down != 0 {
		t.Errorf("after report user1 = (%d,%d), want (0,0)", up, down)
	}
	if up, _ := reg.StatsFor(id2).Snapshot(); up != 0 {
		t.Errorf("after report user2 up = %d, want 0", up)
	}
	if n := len(reg.List()); n != 2 {
		t.Errorf("reset should not remove users, registry size = %d, want 2", n)
	}
}

// 小流量过滤：上下行合计不足 1KB 的不上报；上报成功只清已上报的，
// 被过滤的保留继续累计；过滤后为空直接跳过不发请求.
func TestReportTrafficFiltersTiny(t *testing.T) {
	big := uuid.New()
	tiny := uuid.New()
	reg := user.New([]uuid.UUID{big, tiny})
	reg.StatsFor(big).AddUp(5 * 1024 * 1024)
	reg.StatsFor(tiny).AddUp(100)
	reg.StatsFor(tiny).AddDown(50)

	var gotBody map[string]any
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"recorded":1}`)
	}))
	defer srv.Close()

	if !ReportTraffic(srv.URL+"/api/traffic/report", "id", "k", reg) {
		t.Fatal("reportTraffic returned false")
	}
	users, _ := gotBody["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("body users = %v, want only the big user", gotBody["users"])
	}
	m, _ := users[0].(map[string]any)
	if m["uuid"] != big.String() || m["upBytes"] != float64(5*1024*1024) {
		t.Errorf("reported entry = %v, want big user 5MB up", m)
	}
	// 已上报的清零，被过滤的保留.
	if up, _ := reg.StatsFor(big).Snapshot(); up != 0 {
		t.Errorf("reported user up = %d, want 0", up)
	}
	if up, down := reg.StatsFor(tiny).Snapshot(); up != 100 || down != 50 {
		t.Errorf("filtered user = (%d,%d), want (100,50) preserved", up, down)
	}

	// 全被过滤：零请求、返回成功、计数保留.
	tinyOnly := uuid.New()
	onlyTiny := user.New([]uuid.UUID{tinyOnly})
	onlyTiny.StatsFor(tinyOnly).AddUp(10)
	before := calls
	if !ReportTraffic(srv.URL+"/api/traffic/report", "id", "k", onlyTiny) {
		t.Error("all-filtered should return true (skipped)")
	}
	if calls != before {
		t.Errorf("all-filtered made requests: %d -> %d", before, calls)
	}
	if up, _ := onlyTiny.StatsFor(tinyOnly).Snapshot(); up != 10 {
		t.Errorf("skipped user up = %d, want 10 preserved", up)
	}
}

// 服务端 500 时上报失败返回 false；nil/空注册表直接跳过不发请求.
func TestReportTrafficSkippedAndFailed(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"boom"}`)
	}))
	defer srv.Close()

	failID := uuid.New()
	reg := user.New([]uuid.UUID{failID})
	reg.StatsFor(failID).AddUp(500 * 1024)
	reg.StatsFor(failID).AddDown(600 * 1024)
	if ReportTraffic(srv.URL+"/api/traffic/report", "id", "k", reg) {
		t.Error("reportTraffic with 500 should return false")
	}
	if calls != 1 {
		t.Errorf("requests = %d, want 1", calls)
	}
	// 上报失败不清理：计数保留，下次重试一起带上.
	if up, down := reg.StatsFor(failID).Snapshot(); up != 500*1024 || down != 600*1024 {
		t.Errorf("after failed report = (%d,%d), want (512000,614400) preserved", up, down)
	}

	before := calls
	if !ReportTraffic(srv.URL+"/api/traffic/report", "id", "k", nil) {
		t.Error("nil registry should return true (skipped)")
	}
	if !ReportTraffic(srv.URL+"/api/traffic/report", "id", "k", user.New(nil)) {
		t.Error("empty registry should return true (skipped)")
	}
	if calls != before {
		t.Errorf("skipped reports made requests: %d -> %d", before, calls)
	}
}

// 未配置时 MaybeStart 直接返回，不起协程、不做网络请求。
func TestMaybeStartSkippedWhenUnconfigured(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		MaybeStart(ctx, "", "", BackendInfo{})
		MaybeStart(ctx, "https://dash.example.com", "", BackendInfo{})
		MaybeStart(ctx, "", "123e4567-e89b-12d3-a456-426614174000", BackendInfo{})
		MaybeStart(ctx, "https://dash.example.com", "not-a-uuid", BackendInfo{})
		MaybeStart(ctx, "ftp://dash.example.com", "123e4567-e89b-12d3-a456-426614174000", BackendInfo{})
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("MaybeStart blocked with invalid config")
	}
}
