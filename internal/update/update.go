// Package update 后端程序自更新：dashboard 下发指令后，拉取 GitHub Release
// 新版二进制，验签替换后原地重启（同 PID，systemd 服务不掉线，手动运行也生效，
// 容器内同样生效——替换的是容器内当前二进制）。
// 门禁（见 Supported）：精简构建（notunnel）、托管平台环境、其他系统一律拒绝；
// 自建容器（ Compose / docker run）视为普通 Linux 放行.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	// GitHub 仓库（小写，API 与下载直链通用）.
	githubRepo = "atticus6/go-vless"
	// 单文件下载上限 64MB（二进制约 7MB，防异常大包撑爆内存）.
	maxDownloadBytes = 64 << 20
	httpTimeout      = 30 * time.Second
)

var (
	// 基址做成变量：测试注入 httptest，生产用默认值.
	apiBase      = "https://api.github.com"
	downloadBase = "https://github.com"
	httpClient   = &http.Client{Timeout: httpTimeout}
)

// Result 单次自更新结果：Updated=false 表示已是目标版直接跳过.
type Result struct {
	Updated bool
	From    string
	To      string
}

// managedEnvVars 托管平台注入的环境变量：出现任一即视为 serverless/PaaS
// 托管环境（文件系统随发版丢弃，更新无意义），直接拒绝.
// 注意 DOMAIN/SSL_DOMAIN 是用户自配，不在此列.
var managedEnvVars = []string{
	"VERCEL",
	"VERCEL_URL",
	"NF_HOSTS",
	"RAILWAY_PUBLIC_DOMAIN",
	"KOYEB_PUBLIC_DOMAIN",
}

// Supported 当前环境是否支持自更新：完整构建 + 非托管平台 + linux/darwin.
// 自建容器视为普通 Linux 放行. 不满足返回 false 与原因
// （英文短语，handler 再套 i18n）.
func Supported() (bool, string) {
	if !selfUpdateSupported {
		return false, "slim build has no self-update"
	}
	for _, key := range managedEnvVars {
		if os.Getenv(key) != "" {
			return false, "managed environment (" + key + " is set)"
		}
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return false, "unsupported OS: " + runtime.GOOS
	}
	return true, ""
}

// assetName 目标资源文件名：go-vless-{tag}-{goos}-{goarch}.tar.gz
// （windows 为 .zip；与 release.yml 打包规则一致）.
func assetName(tag string) string {
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("go-vless-%s-%s-%s.%s", tag, runtime.GOOS, runtime.GOARCH, ext)
}

// resolveVersion 定版：显式版直接用；空则问 GitHub latest release 拿 tag.
func resolveVersion(ctx context.Context, target string) (string, error) {
	if target = strings.TrimSpace(target); target != "" {
		return target, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/repos/"+githubRepo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("latest release: http %d", resp.StatusCode)
	}
	var out struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.TagName == "" {
		return "", fmt.Errorf("latest release: bad response")
	}
	return out.TagName, nil
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: http %d: %s", resp.StatusCode, url)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxDownloadBytes {
		return nil, fmt.Errorf("download too large: %s", url)
	}
	return data, nil
}

// parseSums 解析 SHA256SUMS.txt（"hash  filename" 每行），返回 文件名→hash.
func parseSums(raw []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		out[fields[len(fields)-1]] = fields[0]
	}
	return out
}

// extract 从发布包解出二进制：tar.gz 取 go-vless，zip 取 go-vless.exe.
func extract(asset string, data []byte) ([]byte, error) {
	if strings.HasSuffix(asset, ".zip") {
		return extractZip(data)
	}
	return extractTarGz(data)
}

func extractTarGz(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("bad tar.gz: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bad tar.gz: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Name == "go-vless" {
			return io.ReadAll(io.LimitReader(tr, maxDownloadBytes+1))
		}
	}
	return nil, fmt.Errorf("binary not found in tar.gz")
}

func extractZip(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("bad zip: %w", err)
	}
	for _, f := range zr.File {
		if f.Name == "go-vless.exe" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxDownloadBytes+1))
		}
	}
	return nil, fmt.Errorf("binary not found in zip")
}

// replace 原子替换可执行文件：写临时文件 → 旧版挪 .bak → 新版就位；
// 第二步失败回滚 .bak. Linux 下 rename 正在运行的二进制是合法的.
func replace(exePath string, bin []byte) error {
	tmp := exePath + ".new"
	if err := os.WriteFile(tmp, bin, 0755); err != nil {
		return err
	}
	bak := exePath + ".bak"
	_ = os.Remove(bak)
	if err := os.Rename(exePath, bak); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exePath); err != nil {
		_ = os.Rename(bak, exePath)
		return err
	}
	_ = os.Remove(bak)
	return nil
}

// Apply 下载验签并替换二进制，返回结果；只替换不重启
// （重启由 Restart 完成，handler 先回包再重启，避免 dashboard 10 秒超时）。
// current 为注入的构建版本（生产传 status.BuildVersion()）；
// 已是目标版直接跳过（Updated=false，不碰文件）.
func Apply(ctx context.Context, target, current, exePath string) (Result, error) {
	version, err := resolveVersion(ctx, target)
	if err != nil {
		return Result{}, fmt.Errorf("resolve version: %w", err)
	}
	if version == current {
		return Result{Updated: false, From: current, To: version}, nil
	}
	base := downloadBase + "/" + githubRepo + "/releases/download/" + version
	asset := assetName(version)
	sums, err := download(ctx, base+"/SHA256SUMS.txt")
	if err != nil {
		return Result{}, err
	}
	want, ok := parseSums(sums)[asset]
	if !ok {
		return Result{}, fmt.Errorf("checksum missing for %s", asset)
	}
	data, err := download(ctx, base+"/"+asset)
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != strings.ToLower(want) {
		return Result{}, fmt.Errorf("checksum mismatch for %s", asset)
	}
	bin, err := extract(asset, data)
	if err != nil {
		return Result{}, err
	}
	if err := replace(exePath, bin); err != nil {
		return Result{}, fmt.Errorf("replace binary: %w", err)
	}
	return Result{Updated: true, From: current, To: version}, nil
}
