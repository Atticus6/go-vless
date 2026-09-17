package vless

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// 出口连通性探测常量
const (
	probeTimeout    = 3 * time.Second
	dnsTimeout      = 3 * time.Second
	dnsCacheTTL     = 5 * time.Minute
	recheckInterval = 10 * time.Minute
)

// 探测目标: 每个地址族一个主用 + 一个备用, 任一连通即视为该族可用.
var (
	probeIPv4Addrs = []string{"1.1.1.1:80", "8.8.8.8:80"}
	probeIPv6Addrs = []string{"[2606:4700:4700::1111]:80", "[2001:4860:4860::8888]:80"}
)

// monitorEgress 后台维护出口连通性: 启动立即探一次, 之后每 recheckInterval 复探.
// 复探会纠正 learnFromDialError 的锁存 (网络恢复后自愈) 与网络变化, 有变化才打日志.
func (s *Server) monitorEgress() {
	s.refreshEgress(true)
	t := time.NewTicker(recheckInterval)
	defer t.Stop()
	for range t.C {
		s.refreshEgress(false)
	}
}

func (s *Server) refreshEgress(first bool) {
	v4, v6 := detectFamilies()
	changed := first
	if s.hasIPv4.Swap(v4) != v4 {
		changed = true
	}
	if s.hasIPv6.Swap(v6) != v6 {
		changed = true
	}
	if !changed {
		return
	}
	log.Printf("[NET] egress check: IPv4=%s IPv6=%s", okStr(v4), okStr(v6))
	if !v4 || !v6 {
		log.Printf("[NET] targets in unavailable family will be rejected without dial")
	}
}

// learnFromDialError 从 dial 错误中学习: 仅当错误明确证明本机缺该地址族
// (ENETUNREACH/EAFNOSUPPORT 等) 且目标是字面 IP 时, 才锁死对应族、后续直拒.
// 连接拒绝/超时/DNS 等目标侧问题不触发; 域名目标因无法准确归因也不触发.
func (s *Server) learnFromDialError(targetAddr string, err error) {
	if !familyDead(err) {
		return
	}
	host, _, splitErr := net.SplitHostPort(targetAddr)
	if splitErr != nil {
		return
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return
	}
	if ip.To4() != nil {
		if s.hasIPv4.CompareAndSwap(true, false) {
			log.Printf("[NET] egress IPv4 looks unavailable (dial %s: %v), future IPv4 targets will be rejected without dial", targetAddr, err)
		}
		return
	}
	if s.hasIPv6.CompareAndSwap(true, false) {
		log.Printf("[NET] egress IPv6 looks unavailable (dial %s: %v), future IPv6 targets will be rejected without dial", targetAddr, err)
	}
}

// familyDead 判断 err 是否证明本机缺该地址族 (而非目标侧问题).
func familyDead(err error) bool {
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return false
	}
	if opErr.Timeout() {
		return false
	}
	var sysErr *os.SyscallError
	if !errors.As(opErr.Err, &sysErr) {
		return false
	}
	errno, ok := sysErr.Err.(syscall.Errno)
	if !ok {
		return false
	}
	switch errno {
	case syscall.ENETUNREACH, // network is unreachable
		syscall.EAFNOSUPPORT,    // address family not supported
		syscall.EPROTONOSUPPORT, // protocol not supported
		syscall.EADDRNOTAVAIL:   // cannot assign requested address
		return true
	}
	return false
}

// detectFamilies 并行探测本机出口 IPv4/IPv6 连通性.
// 单次调用超时上限约 2*probeTimeout; 由 monitorEgress 周期调用.
func detectFamilies() (v4, v6 bool) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		v4 = probeAddrs(probeIPv4Addrs)
	}()
	go func() {
		defer wg.Done()
		v6 = probeAddrs(probeIPv6Addrs)
	}()
	wg.Wait()
	return v4, v6
}

func probeAddrs(addrs []string) bool {
	for _, addr := range addrs {
		if probeTCP(addr) {
			return true
		}
	}
	return false
}

// probeTCP  bare TCP 建连即算通, 不发任何应用层数据, 成功后立即关闭.
func probeTCP(addr string) bool {
	d := &net.Dialer{Timeout: probeTimeout}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// familyAllowed 按启动探测结果判定目标 host 是否允许建连.
// 字面 IP 直接判族; 域名解析后看是否有任一地址落在可用族 (解析失败则放行, 由后续 dial 自然失败).
func (s *Server) familyAllowed(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return s.hasIPv4.Load()
		}
		return s.hasIPv6.Load()
	}
	ips := lookupCached(host)
	if ips == nil {
		return true
	}
	for _, ip := range ips {
		if ip.To4() != nil && s.hasIPv4.Load() {
			return true
		}
		if ip.To4() == nil && s.hasIPv6.Load() {
			return true
		}
	}
	return false
}

// rejectReason 给日志用的拒绝原因 (与 familyAllowed 同判定, 仅字面 IP 精确).
func (s *Server) rejectReason(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return "no IPv4 egress"
		}
		return "no IPv6 egress"
	}
	return "no reachable address family"
}

type cachedIPs struct {
	ips []net.IP
	exp time.Time
}

var dnsCache sync.Map // host -> cachedIPs, 5 分钟 TTL, 避免每个请求都解析

func lookupCached(host string) []net.IP {
	if v, ok := dnsCache.Load(host); ok {
		if c, ok := v.(cachedIPs); ok && time.Now().Before(c.exp) {
			return c.ips
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		log.Printf("[WARN] DNS lookup %s failed: %v", host, err)
		return nil
	}
	dnsCache.Store(host, cachedIPs{ips: ips, exp: time.Now().Add(dnsCacheTTL)})
	return ips
}
