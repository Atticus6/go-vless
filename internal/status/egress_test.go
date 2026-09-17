package status

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueryEgressIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v4":
			_, _ = w.Write([]byte("203.0.113.7\n"))
		case "/v6":
			_, _ = w.Write([]byte("2001:db8::7\n"))
		case "/garbage":
			_, _ = w.Write([]byte("not an ip"))
		case "/status":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	cases := []struct {
		name   string
		url    string
		wantV6 bool
		want   string
	}{
		{"ipv4 ok", srv.URL + "/v4", false, "203.0.113.7"},
		{"ipv6 ok", srv.URL + "/v6", true, "2001:db8::7"},
		// 地址族不符直接丢弃
		{"ipv4 as v6 rejected", srv.URL + "/v4", true, ""},
		{"ipv6 as v4 rejected", srv.URL + "/v6", false, ""},
		{"garbage rejected", srv.URL + "/garbage", false, ""},
		{"non-200 rejected", srv.URL + "/status", false, ""},
		{"unreachable rejected", "http://127.0.0.1:1/", false, ""},
	}

	for _, c := range cases {
		if got := queryEgressIP(c.url, c.wantV6); got != c.want {
			t.Errorf("%s: queryEgressIP = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFetchEgressIPFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/down" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("198.51.100.9"))
	}))
	defer srv.Close()

	// 主用挂了走备用
	if got := fetchEgressIP([]string{srv.URL + "/down", srv.URL + "/up"}, false); got != "198.51.100.9" {
		t.Errorf("fallback = %q, want 198.51.100.9", got)
	}
	// 全挂返回空
	if got := fetchEgressIP([]string{srv.URL + "/down"}, false); got != "" {
		t.Errorf("all down = %q, want empty", got)
	}
}

func TestEgressIPUnknownByDefault(t *testing.T) {
	p := &Provider{}
	if got := p.egressIP(false); got != "unknown" {
		t.Errorf("egressIPv4 default = %q, want unknown", got)
	}
	if got := p.egressIP(true); got != "unknown" {
		t.Errorf("egressIPv6 default = %q, want unknown", got)
	}
}

func TestRefreshEgressOnDemand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("203.0.113.7"))
	}))
	old := egressIPv4Services
	egressIPv4Services = []string{srv.URL}
	defer func() { egressIPv4Services = old }()

	// v6 关掉, 避免单测碰真实外网
	p := &Provider{IPv6Supported: func() bool { return false }}
	p.refreshEgress()
	if got := p.egressIP(false); got != "203.0.113.7" {
		t.Fatalf("after refresh = %q, want 203.0.113.7", got)
	}
	// 缓存期内服务挂了也照样返回缓存值
	srv.Close()
	p.refreshEgress()
	if got := p.egressIP(false); got != "203.0.113.7" {
		t.Errorf("cached = %q, want 203.0.113.7", got)
	}
}

func TestRefreshEgressSkipsUnsupported(t *testing.T) {
	never := func() bool { return false }
	p := &Provider{IPv4Supported: never, IPv6Supported: never}
	p.refreshEgress()
	if got := p.egressIP(true); got != "unknown" {
		t.Errorf("unsupported v6 = %q, want unknown", got)
	}
	if got := p.egressIP(false); got != "unknown" {
		t.Errorf("unsupported v4 = %q, want unknown", got)
	}
}
