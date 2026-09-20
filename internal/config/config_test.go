package config

import "testing"

// ssl-domain 归一化与校验：空=不启用；大小写/首尾空格/尾点归一；
// 带 scheme、路径、端口、空白、非法字符、单标签一律拒绝.
func TestParseDomain(t *testing.T) {
	valid := map[string]string{
		"":                   "",
		"example.com":        "example.com",
		"Example.COM":        "example.com",
		"  sub.example.com ": "sub.example.com",
		"example.com.":       "example.com",
		"a-b.c123.com":       "a-b.c123.com",
	}
	for in, want := range valid {
		if got, err := parseDomain(in); err != nil || got != want {
			t.Errorf("parseDomain(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	invalid := []string{
		"https://example.com",
		"http://example.com/x",
		"example.com/path",
		"example.com:443",
		"example .com",
		"exam_ple.com",
		"localhost",
		"example",
		"-bad.com",
		"bad-.com",
		"a..com",
		".example.com",
	}
	for _, in := range invalid {
		if got, err := parseDomain(in); err == nil {
			t.Errorf("parseDomain(%q) = %q, nil; want error", in, got)
		}
	}
}
