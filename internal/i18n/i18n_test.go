package i18n

import (
	"os"
	"regexp"
	"testing"
)

// printfVerbs 提取模板中的格式化动词序列 (%v/%s/%q/%d/%.200s 等, %% 除外).
var printfVerbs = regexp.MustCompile(`%(\.\d+)?[vstqdxXoUeEfFgGpcU]`)

func verbs(s string) string {
	// 先去掉转义的 %% 再按序提取全部动词
	clean := regexp.MustCompile(`%%`).ReplaceAllString(s, "")
	found := printfVerbs.FindAllString(clean, -1)
	joined := ""
	for i, v := range found {
		if i > 0 {
			joined += ","
		}
		joined += v
	}
	return joined
}

func TestLocalesParity(t *testing.T) {
	en, ok := messages[LangEN]
	if !ok || len(en) == 0 {
		t.Fatalf("en locale empty, embed load failed?")
	}
	zh, ok := messages[LangZH]
	if !ok || len(zh) == 0 {
		t.Fatalf("zh locale empty, embed load failed?")
	}
	for k := range en {
		z, ok := zh[k]
		if !ok {
			t.Errorf("zh missing key %q", k)
			continue
		}
		if verbs(en[k]) != verbs(z) {
			t.Errorf("key %q verbs differ: en=%q zh=%q", k, verbs(en[k]), verbs(z))
		}
	}
	for k := range zh {
		if _, ok := en[k]; !ok {
			t.Errorf("en missing key %q", k)
		}
	}
}

func withEnv(t *testing.T, k, v string) {
	t.Helper()
	old, had := os.LookupEnv(k)
	if err := os.Setenv(k, v); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(k, old)
		} else {
			_ = os.Unsetenv(k)
		}
	})
}

func clearLangEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GO_VLESS_LANG", "LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		old, had := os.LookupEnv(k)
		_ = os.Unsetenv(k)
		if had {
			v := old
			t.Cleanup(func() { _ = os.Setenv(k, v) })
		}
	}
}

func TestDetectDefaultEnglish(t *testing.T) {
	clearLangEnv(t)
	if got := detectFromEnv(); got != LangEN {
		t.Fatalf("default = %q, want en", got)
	}
}

func TestDetectChinese(t *testing.T) {
	clearLangEnv(t)
	withEnv(t, "LANG", "zh_CN.UTF-8")
	if got := detectFromEnv(); got != LangZH {
		t.Fatalf("LANG zh = %q, want zh", got)
	}
}

func TestDetectPriorityLCAll(t *testing.T) {
	clearLangEnv(t)
	withEnv(t, "LANG", "zh_CN.UTF-8")
	withEnv(t, "LC_ALL", "en_US.UTF-8")
	if got := detectFromEnv(); got != LangEN {
		t.Fatalf("LC_ALL should win, got %q", got)
	}
}

func TestDetectExplicitOverride(t *testing.T) {
	clearLangEnv(t)
	withEnv(t, "LANG", "en_US.UTF-8")
	withEnv(t, "GO_VLESS_LANG", "zh")
	if got := detectFromEnv(); got != LangZH {
		t.Fatalf("GO_VLESS_LANG should win, got %q", got)
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"zh": "zh", "zh-CN": "zh", "zh_CN.UTF-8": "zh", "ZH": "zh",
		"en": "en", "en-US": "en", "C.UTF-8": "en", "fr_FR": "en", "": "en",
	}
	clearLangEnv(t)
	for in, want := range cases {
		if in == "" {
			continue // empty falls back to env
		}
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSetAndGet(t *testing.T) {
	defer Set(detectFromEnv())
	if Set("zh") != LangZH || Get() != LangZH {
		t.Fatal("Set(zh) failed")
	}
	if Set("en") != LangEN || Get() != LangEN {
		t.Fatal("Set(en) failed")
	}
	if Set("xx") != LangEN {
		t.Fatal("invalid lang should fall back to en")
	}
}

func TestTranslationAndFallback(t *testing.T) {
	defer Set(detectFromEnv())
	Set("zh")
	if got := T("server.stopped"); got != "服务已停止" {
		t.Fatalf("zh server.stopped = %q", got)
	}
	Set("en")
	if got := T("server.stopped"); got != "Server stopped" {
		t.Fatalf("en server.stopped = %q", got)
	}
	if got := T("no.such.key"); got != "no.such.key" {
		t.Fatalf("missing key should echo, got %q", got)
	}
	if got := Sprintf("server.listening", 8080); got != "VLESS Server listening on :8080" {
		t.Fatalf("Sprintf en = %q", got)
	}
	if got := SprintfFor("zh", "server.listening", 8080); got != "VLESS 服务已在 :8080 上监听" {
		t.Fatalf("SprintfFor zh = %q", got)
	}
}

func TestAcceptLanguage(t *testing.T) {
	cases := map[string]string{
		"": "en", "en-US,en;q=0.9": "en", "zh-CN,zh;q=0.9": "zh",
		"fr-FR,zh;q=0.8": "zh", "ja": "en", "zh": "zh",
	}
	for in, want := range cases {
		if got := LangFromAcceptLanguage(in); got != want {
			t.Errorf("LangFromAcceptLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}
