package register

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 注册 wire 格式：POST {dashboard}/api/nodes/register，JSON {id,key,version}，
// 成功回 {"ok":true} 即停。
func TestReportSuccess(t *testing.T) {
	var gotMethod, gotPath, gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotCT = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"userTokens":["tok-1"]}`)
	}))
	defer srv.Close()

	ok := report(
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

// 401（key 对不上）只返回失败，不抛、不改周期。
func TestReportUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
	}))
	defer srv.Close()

	if report(srv.URL+"/api/nodes/register", "123e4567-e89b-12d3-a456-426614174000", "wrong", BackendInfo{}) {
		t.Error("report with 401 should return not-ok")
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
