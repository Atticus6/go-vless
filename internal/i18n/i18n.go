// Package i18n 为 go-vless 提供零依赖中英双语支持.
//
// 文案在 locales/*.json 中按语言独立存放 (与 dashboard 前端风格一致),
// 经 go:embed 编进二进制, 无外部文件依赖.
//
// 语言决定顺序: 显式 Set (对应 --lang / GO_VLESS_LANG) > LC_ALL >
// LC_MESSAGES > LANG > LANGUAGE, 值以 zh 开头 (zh, zh_CN.UTF-8 等)
// 即为中文, 其余回退英文. 默认英文, 保持现有日志/监控抓取兼容,
// 中文用户设 LANG=zh_CN.UTF-8 (或 --lang zh) 即可切换.
//
// HTTP 面向用户的报错另支持 Accept-Language 按请求协商,
// 见 LangFromAcceptLanguage / SprintfFor.
package i18n

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

const (
	LangEN = "en"
	LangZH = "zh"
)

//go:embed locales/en.json
var enJSON []byte

//go:embed locales/zh.json
var zhJSON []byte

// messages 启动时从 embed 的 JSON 解析, 解析失败回退空表 (get 再回退 key 本身).
var messages = loadMessages()

func loadMessages() map[string]map[string]string {
	out := map[string]map[string]string{}
	for lang, data := range map[string][]byte{LangEN: enJSON, LangZH: zhJSON} {
		m := map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			m = map[string]string{}
		}
		out[lang] = m
	}
	return out
}

// current 为进程全局语言, Detect 失败时回退英文.
var current atomic.Value // string

func init() {
	current.Store(detectFromEnv())
}

// Set 显式设置进程语言, 非法值回退英文. 返回生效后的语言.
func Set(lang string) string {
	lang = Normalize(lang)
	current.Store(lang)
	return lang
}

// Get 返回当前进程语言 ("zh" 或 "en").
func Get() string {
	if v, ok := current.Load().(string); ok && v != "" {
		return v
	}
	return LangEN
}

// Normalize 将任意语言标记归一化为 zh/en, 未知回退 en.
func Normalize(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	// 兼容 zh_CN.UTF-8 / zh-CN / zh_CN:utf8 等写法
	l = strings.ReplaceAll(l, "_", "-")
	if l == "" || l == "auto" {
		return detectFromEnv()
	}
	if l == "zh" || strings.HasPrefix(l, "zh-") || strings.HasPrefix(l, "zh:") {
		return LangZH
	}
	if l == "cn" || strings.HasPrefix(l, "cn-") {
		return LangZH
	}
	return LangEN
}

// detectFromEnv 按 LC_ALL > LC_MESSAGES > LANG > LANGUAGE 探测.
func detectFromEnv() string {
	for _, key := range []string{"GO_VLESS_LANG", "LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" || v == "C" || v == "POSIX" {
			continue
		}
		// LANGUAGE 可为冒号分隔的优先级列表, 取首个
		if key == "LANGUAGE" {
			v = strings.TrimSpace(strings.Split(v, ":")[0])
		}
		l := strings.ToLower(strings.ReplaceAll(v, "_", "-"))
		if l == "" {
			continue
		}
		if l == "zh" || strings.HasPrefix(l, "zh-") {
			return LangZH
		}
		// 显式英文直接定死, 避免继续看后面的变量
		if l == "en" || strings.HasPrefix(l, "en-") {
			return LangEN
		}
		// 其他语言 (ja/de/...) 暂无翻译, 回退英文
		if isLanguageTag(l) {
			return LangEN
		}
	}
	return LangEN
}

// isLanguageTag 粗略判断是否为语言标记, 避免把随机字符串误判.
func isLanguageTag(s string) bool {
	if len(s) < 2 {
		return false
	}
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || c == '-' || c == '.' || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

// LangFromAcceptLanguage 按 HTTP Accept-Language 头协商.
// 双语应用的务实策略: 头部任一位置含 zh 即中文, 否则英文
// (无头/解析失败回退英文).
func LangFromAcceptLanguage(header string) string {
	for _, part := range strings.Split(header, ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		tag = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"))
		if tag == "zh" || strings.HasPrefix(tag, "zh-") {
			return LangZH
		}
	}
	return LangEN
}

// T 取 key 在 lang 下的文案, 缺失回退英文, 再缺失返回 key 本身.
func T(key string) string {
	return get(Get(), key)
}

// TFor 取 key 在指定语言下的文案.
func TFor(lang, key string) string {
	return get(Normalize(lang), key)
}

// Sprintf 取模板并格式化 (当前语言).
func Sprintf(key string, args ...any) string {
	return fmt.Sprintf(T(key), args...)
}

// SprintfFor 取模板并格式化 (指定语言).
func SprintfFor(lang, key string, args ...any) string {
	return fmt.Sprintf(TFor(lang, key), args...)
}

func get(lang, key string) string {
	if m, ok := messages[lang]; ok {
		if s, ok := m[key]; ok {
			return s
		}
	}
	if s, ok := messages[LangEN][key]; ok {
		return s
	}
	return key
}
