//go:build !notunnel

package tunnel

import (
	"context"
	"log"
	"sync"

	"github.com/atticus6/go-vless/internal/i18n"
)

// Tunnel 管理 Cloudflare Argo 隧道 (与 apps/node/tunnel 保持一致)
type Tunnel struct {
	localPort int
	protocol  string
	url       string
	cancel    context.CancelFunc
	mu        sync.RWMutex
}

// New 创建新的隧道实例.
// protocol: auto(默认, 官方推荐) | quic(UDP) | http2(TCP).
// 当 edge TCP 握手被 RST 时可尝试 quic; 反之亦然.
func New(localPort int, protocol string) *Tunnel {
	if protocol == "" {
		protocol = "auto"
	}
	return &Tunnel{localPort: localPort, protocol: protocol}
}

// Start 启动 Argo 隧道
func (t *Tunnel) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel

	url, err := CreateCloudflareTunnel(ctx, t.localPort, t.protocol)
	if err != nil {
		cancel()
		return err
	}

	t.mu.Lock()
	t.url = url
	t.mu.Unlock()

	log.Printf(i18n.T("tunnel.established"), url)
	return nil
}

// Stop 停止隧道
func (t *Tunnel) Stop() {
	if t.cancel != nil {
		t.cancel()
		log.Println(i18n.T("tunnel.stopped"))
	}
}

// GetURL 获取隧道 URL
func (t *Tunnel) GetURL() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.url
}
