//go:build !notunnel

package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// 白名单只放行本域名, 其他一律拒绝 (防陌生域名蹭证书).
func TestAutocertHostPolicy(t *testing.T) {
	m := newAutocertManager("example.com", t.TempDir())
	if err := m.HostPolicy(context.Background(), "example.com"); err != nil {
		t.Errorf("own domain rejected: %v", err)
	}
	for _, host := range []string{"evil.com", "sub.example.com", "example.com.evil.com", ""} {
		if err := m.HostPolicy(context.Background(), host); err == nil {
			t.Errorf("host %q should be rejected", host)
		}
	}
}

// certExpiry 只读缓存：命中返回到期时间；缺失/损坏返回 false，绝不触发申请.
func TestCertExpiryFromCache(t *testing.T) {
	dir := t.TempDir()
	cache := autocert.DirCache(dir)
	if _, ok := certExpiry(cache, "example.com"); ok {
		t.Error("empty cache should miss")
	}
	if _, ok := certExpiry(nil, "example.com"); ok {
		t.Error("nil cache should miss")
	}

	notAfter := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)
	pemBytes := selfSignedPEM(t, "example.com", notAfter)
	ctx := context.Background()
	if err := cache.Put(ctx, "example.com", pemBytes); err != nil {
		t.Fatalf("cache put: %v", err)
	}
	got, ok := certExpiry(cache, "example.com")
	if !ok {
		t.Fatal("cached cert should hit")
	}
	if !got.Equal(notAfter) {
		t.Errorf("expiry = %v, want %v", got, notAfter)
	}
	// 大小写/尾点归一后同样命中.
	if _, ok := certExpiry(cache, "Example.COM."); !ok {
		t.Error("normalized domain should hit")
	}
	// 坏数据跳过.
	if err := cache.Put(ctx, "bad.example.com", []byte("not-pem")); err != nil {
		t.Fatalf("cache put: %v", err)
	}
	if _, ok := certExpiry(cache, "bad.example.com"); ok {
		t.Error("corrupt cache entry should miss")
	}
}

func selfSignedPEM(t *testing.T, domain string, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return append(certPEM, keyPEM...)
}
