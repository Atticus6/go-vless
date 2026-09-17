package vless

import (
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atticus6/go-vless/internal/user"
	"github.com/gorilla/websocket"
)

// 会话转发调优常量
const (
	bufferSize   = 32 * 1024
	udpBufferMax = 64*1024 + 2      // UDP datagram + 2B 长度头
	writeTimeout = 10 * time.Second // WS 单次写超时, 避免慢客户端拖住 wsMu
	idleTimeout  = 120 * time.Second

	wsReadDeadline = 60 * time.Second
	pingInterval   = 30 * time.Second
	dialTimeout    = 10 * time.Second

	// 高并发保护
	wsReadLimit = 256 * 1024 // 单个 WS message 上限, 防 OOM
	wsBufSize   = 4 * 1024   // upgrader 内核缓冲, 4096 足够, 之前 32K x 2 x N 太吃内存
)

// buffer 池: 避免每连接 32K/64K 反复分配, 降低 GC 压力
var (
	tcpBufPool = sync.Pool{New: func() any {
		b := make([]byte, bufferSize)
		return b
	}}
	udpBufPool = sync.Pool{New: func() any {
		b := make([]byte, udpBufferMax)
		return b
	}}
)

// dialer 复用: KeepAlive + 超时, 避免每次新建
var tcpDialer = &net.Dialer{
	Timeout:   dialTimeout,
	KeepAlive: 30 * time.Second,
	DualStack: true,
}
var udpDialer = &net.Dialer{
	Timeout:   dialTimeout,
	DualStack: true,
}

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  wsBufSize,
	WriteBufferSize: wsBufSize,
}

// Server VLESS over WebSocket 接入服务.
// hasIPv4/hasIPv6 为出口连通性 (后台探测 + dial 失败学习共同维护), 建连前据此快速拒绝不可达族.
type Server struct {
	users   *user.Registry
	sem     chan struct{}
	max     int
	hasIPv4 atomic.Bool
	hasIPv6 atomic.Bool
}

// New 创建接入服务, maxConns 为最大并发 VLESS 会话数.
// 出口探测放后台异步跑, 不阻塞监听: 冷启动零增加; 探测完成前按双栈可用放行.
// dial 失败若证明缺某地址族会即时锁存, 周期复探负责自愈与跟随网络变化.
func New(users *user.Registry, maxConns int) *Server {
	if maxConns <= 0 {
		maxConns = 4096
	}
	s := &Server{users: users, sem: make(chan struct{}, maxConns), max: maxConns}
	s.hasIPv4.Store(true)
	s.hasIPv6.Store(true)
	go s.monitorEgress()
	return s
}

func okStr(ok bool) string {
	if ok {
		return "ok"
	}
	return "unavailable"
}

// HasIPv4/HasIPv6 当前出口是否支持该地址族 (后台探测 + dial 失败学习共同维护).
func (s *Server) HasIPv4() bool { return s.hasIPv4.Load() }
func (s *Server) HasIPv6() bool { return s.hasIPv6.Load() }

// Handler WebSocket 握手 + 全局限流 + 会话分发, 可直接挂到 mux 上.
func (s *Server) Handler(w http.ResponseWriter, r *http.Request) {
	if strings.ToLower(r.Header.Get("Upgrade")) != "websocket" {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte("Bad Request"))
		} else {
			log.Printf("[WARN] Expected WebSocket, got Upgrade: %s path: %s", r.Header.Get("Upgrade"), r.URL.Path)
			http.Error(w, "Expected WebSocket", http.StatusUpgradeRequired)
		}
		return
	}

	// 全局限流: 满了直接 503, 避免 fd/goroutine 被打爆
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		log.Printf("[WARN] Server busy, reject %s (max-conn=%d)", r.RemoteAddr, s.max)
		http.Error(w, "Server busy", http.StatusServiceUnavailable)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ERROR] WebSocket upgrade failed: %v", err)
		return
	}

	// 防大 message OOM (首包头 + 后续转发共用)
	ws.SetReadLimit(wsReadLimit)
	s.handleSession(ws, r.RemoteAddr)
}

func (s *Server) handleSession(ws *websocket.Conn, clientAddr string) {
	// 解析失败等提前返回路径也保证关闭连接 (正常路径由 session.cleanup 关, 重复 Close 安全)
	defer ws.Close()

	// 设置 ping/pong 保活 (首包读取也受读超时保护)
	ws.SetReadDeadline(time.Now().Add(wsReadDeadline))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(wsReadDeadline))
		return nil
	})

	// 读取 VLESS 请求头 (含 UUID 认证)
	_, headerData, err := ws.ReadMessage()
	if err != nil {
		if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure, websocket.CloseNoStatusReceived) {
			log.Printf("[ERROR] Failed to read VLESS header from %s: %v", clientAddr, err)
		}
		return
	}

	reqID, targetAddr, command, payload, err := parseRequest(headerData, s.users)
	if err != nil {
		log.Printf("[ERROR] Invalid VLESS request from %s: %v", clientAddr, err)
		return
	}

	session := newSession(ws, clientAddr, s.users.StatsFor(reqID))
	defer session.cleanup()

	go session.startPingLoop()

	// 地址族预检: 出口不支持该族直接拒绝, 省掉 dial 等待
	host, _, err := net.SplitHostPort(targetAddr)
	if err != nil {
		log.Printf("[ERROR] Invalid target addr %q from %s: %v", targetAddr, clientAddr, err)
		return
	}
	if !s.familyAllowed(host) {
		log.Printf("[WARN] Reject %s from %s: %s", targetAddr, clientAddr, s.rejectReason(host))
		return
	}

	switch command {
	case cmdTCP:
		s.handleTCPSession(session, targetAddr, payload)
	case cmdUDP:
		s.handleUDPSession(session, targetAddr, payload)
	default:
		log.Printf("[WARN] Unsupported command: %d", command)
	}
}
