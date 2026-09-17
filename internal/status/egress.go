package status

import (
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// 出口 IP 查询常量
const (
	egressTimeout  = 5 * time.Second
	egressCacheTTL = 10 * time.Minute
)

// 查询服务: 主用 + 备用, 纯文本返回 IP
var (
	egressIPv4Services = []string{"https://api.ipify.org", "https://ipv4.icanhazip.com"}
	egressIPv6Services = []string{"https://api64.ipify.org", "https://ipv6.icanhazip.com"}
)

var egressHTTP = &http.Client{Timeout: egressTimeout}

type egressCache struct {
	ip  string
	exp time.Time
}

// refreshEgress 按需刷新出口 IP, 由 /{uuid} 请求触发.
// TTL 内直接用缓存; 查询失败不写缓存 (下次请求重试); 不支持的地址族直接跳过.
func (p *Provider) refreshEgress() {
	now := time.Now()
	if c, ok := p.egressV4.Load().(egressCache); !ok || now.After(c.exp) {
		if !boolOr(p.IPv4Supported, true) {
			p.egressV4.Store(egressCache{exp: now.Add(egressCacheTTL)})
		} else if v4 := fetchEgressIP(egressIPv4Services, false); v4 != "" {
			p.egressV4.Store(egressCache{ip: v4, exp: now.Add(egressCacheTTL)})
		}
	}
	if c, ok := p.egressV6.Load().(egressCache); !ok || now.After(c.exp) {
		if !boolOr(p.IPv6Supported, true) {
			p.egressV6.Store(egressCache{exp: now.Add(egressCacheTTL)})
		} else if v6 := fetchEgressIP(egressIPv6Services, true); v6 != "" {
			p.egressV6.Store(egressCache{ip: v6, exp: now.Add(egressCacheTTL)})
		}
	}
}

func (p *Provider) egressIP(v6 bool) string {
	av := &p.egressV4
	if v6 {
		av = &p.egressV6
	}
	if c, ok := av.Load().(egressCache); ok && c.ip != "" {
		return c.ip
	}
	return "unknown"
}

func fetchEgressIP(services []string, wantV6 bool) string {
	for _, svc := range services {
		if ip := queryEgressIP(svc, wantV6); ip != "" {
			return ip
		}
	}
	return ""
}

// queryEgressIP 查单个服务, 严格校验: 必须是合法 IP 且地址族符合预期
func queryEgressIP(url string, wantV6 bool) string {
	resp, err := egressHTTP.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}
	ip := net.ParseIP(strings.TrimSpace(string(body)))
	if ip == nil {
		return ""
	}
	if (ip.To4() == nil) != wantV6 {
		return ""
	}
	return ip.String()
}
