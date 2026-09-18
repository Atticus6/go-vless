//go:build notunnel

package tunnel

import (
	"context"
	"log"

	"github.com/atticus6/go-vless/internal/i18n"
)

// Tunnel 是 cloudflared 隧道的空实现.
// 用 `-tags notunnel` 构建时 (如 Vercel) 不引入 cloudflared 依赖,
// Start 直接跳过, 与 main.go 的调用保持 API 兼容.
type Tunnel struct {
	localPort int
	protocol  string
}

// New 创建隧道实例 (notunnel 下仅保存参数, 不做任何事).
func New(localPort int, protocol string) *Tunnel {
	if protocol == "" {
		protocol = "auto"
	}
	return &Tunnel{localPort: localPort, protocol: protocol}
}

// Start 在 notunnel 构建下直接跳过, 返回 nil 让 main 流程不受影响.
func (t *Tunnel) Start(_ context.Context) error {
	log.Println(i18n.T("tunnel.notunnel"))
	return nil
}

// Stop 空实现, 保持 API 兼容.
func (t *Tunnel) Stop() {}

// GetURL 空实现下永远返回空字符串.
func (t *Tunnel) GetURL() string { return "" }
