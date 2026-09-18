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
