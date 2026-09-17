package status

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/atticus6/go-vless/internal/user"
	"github.com/google/uuid"
)

// testMux 按生产接线组装 (中间件一起测)
func testMux(p *Provider) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/config", WithConfigKey(p.Handler))
	mux.HandleFunc("POST /config/users/add", WithConfigKey(p.AddUsersHandler))
	mux.HandleFunc("POST /config/users/remove", WithConfigKey(p.RemoveUsersHandler))
	return mux
}

func TestConfigHandlerAuth(t *testing.T) {
	id := uuid.New()
	users := user.New([]uuid.UUID{id})
	users.StatsFor(id).AddUp(1024)
	never := func() bool { return false }
	p := &Provider{Users: users, StartedAt: time.Now(), IPv4Supported: never, IPv6Supported: never}
	mux := testMux(p)

	call := func(key, header string) *httptest.ResponseRecorder {
		url := "/config"
		if key != "" {
			url += "?key=" + key
		}
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if header != "" {
			req.Header.Set("X-Config-Key", header)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 未设 CONFIG_KEY: 一律 404 (即使带 key)
	t.Setenv("CONFIG_KEY", "")
	if rec := call("anything", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unset key: code = %d, want 404", rec.Code)
	}

	t.Setenv("CONFIG_KEY", "s3cr3t")

	if rec := call("", ""); rec.Code != http.StatusNotFound {
		t.Errorf("no key: code = %d, want 404", rec.Code)
	}
	if rec := call("wrong", ""); rec.Code != http.StatusNotFound {
		t.Errorf("wrong key: code = %d, want 404", rec.Code)
	}

	// query key 通过
	rec := call("s3cr3t", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("query key: code = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	usersBody, ok := body["users"].(map[string]any)
	if !ok {
		t.Fatalf("missing users map: %v", body)
	}
	entry, ok := usersBody[id.String()].(map[string]any)
	if !ok || entry["up"] != "1.00 KB" || entry["down"] != "0 B" {
		t.Errorf("traffic entry = %v, want up=1.00 KB down=0 B", entry)
	}

	// header key 通过
	if rec := call("", "s3cr3t"); rec.Code != http.StatusOK {
		t.Errorf("header key: code = %d, want 200", rec.Code)
	}
}

func TestConfigUsersManage(t *testing.T) {
	t.Setenv("CONFIG_KEY", "s3cr3t")
	id := uuid.New()
	users := user.New([]uuid.UUID{id})
	p := &Provider{Users: users, StartedAt: time.Now()}
	mux := testMux(p)

	post := func(path, body, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path+"?key="+key, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 鉴权失败 404 (add/remove 都是)
	if rec := post("/config/users/add", `{"uuids":[]}`, "bad"); rec.Code != http.StatusNotFound {
		t.Errorf("add wrong key: code = %d, want 404", rec.Code)
	}
	if rec := post("/config/users/remove", `{"uuids":[]}`, ""); rec.Code != http.StatusNotFound {
		t.Errorf("remove no key: code = %d, want 404", rec.Code)
	}

	// 非法 UUID 400 且不生效
	newID := uuid.New()
	if rec := post("/config/users/add", `{"uuids":["`+newID.String()+`","zzz"]}`, "s3cr3t"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid uuid: code = %d, want 400", rec.Code)
	}
	if users.Valid(newID) {
		t.Errorf("invalid batch partially applied")
	}

	// 正常新增
	rec := post("/config/users/add", `{"uuids":["`+newID.String()+`"]}`, "s3cr3t")
	if rec.Code != http.StatusOK {
		t.Fatalf("add: code = %d, want 200", rec.Code)
	}
	var added struct {
		OK    bool     `json:"ok"`
		Users []string `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil || !added.OK || len(added.Users) != 2 {
		t.Fatalf("add response = %q, want ok+2 users", rec.Body.String())
	}
	if !users.Valid(newID) {
		t.Errorf("added user not valid")
	}

	// 删除 (含不存在的, 幂等)
	if rec := post("/config/users/remove", `{"uuids":["`+newID.String()+`","`+uuid.New().String()+`"]}`, "s3cr3t"); rec.Code != http.StatusOK {
		t.Fatalf("remove: code = %d, want 200", rec.Code)
	}
	if users.Valid(newID) {
		t.Errorf("removed user still valid")
	}
	if !users.Valid(id) {
		t.Errorf("original user lost")
	}
}
