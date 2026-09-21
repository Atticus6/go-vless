package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 精简构建/serverless/托管平台一律拒绝：平台变量用环境变量稳定复现.
func TestSupportedServerless(t *testing.T) {
	t.Setenv("VERCEL", "1")
	if ok, _ := Supported(); ok {
		t.Error("serverless should not support self-update")
	}
}

// 托管平台注入变量出现任一即拒绝（Vercel/Netlify/Railway/Koyeb）。
func TestSupportedManagedEnv(t *testing.T) {
	for _, key := range []string{
		"VERCEL_URL",
		"NF_HOSTS",
		"RAILWAY_PUBLIC_DOMAIN",
		"KOYEB_PUBLIC_DOMAIN",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "example.com")
			ok, reason := Supported()
			if ok {
				t.Errorf("%s set should not support self-update", key)
			}
			if reason == "" {
				t.Error("refusal should carry a reason")
			}
		})
	}
}

// 显式版本直接采用，不发任何网络请求.
func TestResolveExplicit(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer srv.Close()
	oldAPI := apiBase
	apiBase = srv.URL
	defer func() { apiBase = oldAPI }()

	v, err := resolveVersion(context.Background(), "v9.9.9")
	if err != nil || v != "v9.9.9" {
		t.Fatalf("resolve = %q,%v, want v9.9.9,nil", v, err)
	}
	if calls != 0 {
		t.Errorf("explicit version made %d requests, want 0", calls)
	}
}

// fakeRelease 搭一个完整假 release：latest/tag、sums、tar.gz 二进制包.
// 资源名按本机运行时拼（assetName），跨平台一致；
// badSums=true 时 sums 写错值（包本身是对的），测验签失败.
func fakeRelease(t *testing.T, mux *http.ServeMux, tag string, bin []byte, badSums bool) {
	t.Helper()
	asset := assetName(tag)
	var pkg []byte
	if strings.HasSuffix(asset, ".zip") {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		fw, err := zw.Create("go-vless.exe")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(bin); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		pkg = buf.Bytes()
	} else {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: "go-vless", Mode: 0755, Size: int64(len(bin))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(bin); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		pkg = buf.Bytes()
	}
	sum := sha256.Sum256(pkg)
	want := hex.EncodeToString(sum[:])
	if badSums {
		want = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	}
	mux.HandleFunc("/repos/"+githubRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q}`, tag)
	})
	mux.HandleFunc("/"+githubRepo+"/releases/download/"+tag+"/SHA256SUMS.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", want, asset)
	})
	mux.HandleFunc("/"+githubRepo+"/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		w.Write(pkg)
	})
}

// 全流程：定版 latest → 验签 → 替换，返回 from/to；旧文件内容被换掉.
func TestApplyFullFlow(t *testing.T) {
	mux := http.NewServeMux()
	fakeRelease(t, mux, "v9.9.9", []byte("new-binary"), false)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	oldAPI, oldDL := apiBase, downloadBase
	apiBase, downloadBase = srv.URL, srv.URL
	defer func() { apiBase, downloadBase = oldAPI, oldDL }()

	exe := filepath.Join(t.TempDir(), "go-vless")
	if err := os.WriteFile(exe, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(context.Background(), "", "dev", exe)
	if err != nil {
		t.Fatalf("Apply = %v", err)
	}
	if !res.Updated || res.From != "dev" || res.To != "v9.9.9" {
		t.Errorf("result = %+v, want updated dev->v9.9.9", res)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "new-binary" {
		t.Errorf("binary = %q, want new-binary", got)
	}
}

// 已是目标版直接跳过：零请求、不碰文件.
func TestApplyAlreadyCurrent(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer srv.Close()
	oldAPI, oldDL := apiBase, downloadBase
	apiBase, downloadBase = srv.URL, srv.URL
	defer func() { apiBase, downloadBase = oldAPI, oldDL }()

	exe := filepath.Join(t.TempDir(), "go-vless")
	if err := os.WriteFile(exe, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(context.Background(), "v1.0.0", "v1.0.0", exe)
	if err != nil {
		t.Fatalf("Apply = %v", err)
	}
	if res.Updated {
		t.Errorf("already current should not update: %+v", res)
	}
	if calls != 0 {
		t.Errorf("skip made %d requests, want 0", calls)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "old-binary" {
		t.Errorf("binary touched: %q", got)
	}
}

// 验签失败：报错且原文件不动.
func TestApplyBadChecksum(t *testing.T) {
	mux := http.NewServeMux()
	fakeRelease(t, mux, "v9.9.9", []byte("new-binary"), true)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	oldAPI, oldDL := apiBase, downloadBase
	apiBase, downloadBase = srv.URL, srv.URL
	defer func() { apiBase, downloadBase = oldAPI, oldDL }()

	exe := filepath.Join(t.TempDir(), "go-vless")
	if err := os.WriteFile(exe, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), "", "dev", exe); err == nil {
		t.Fatal("bad checksum should fail")
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "old-binary" {
		t.Errorf("binary touched: %q", got)
	}
}
