package vless

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atticus6/go-vless/internal/user"
	"github.com/gorilla/websocket"
)

// session 封装 VLESS 会话状态
// 高并发设计:
// - wsMu 只串行化 WS 写 (data + ping), 不阻塞 remote 读写
// - remoteConn 赋值后只由固定 goroutine 使用, 数据路径无锁
// - closed 用 atomic, isClosed() 无锁, 避免每包抢锁
type session struct {
	ws         *websocket.Conn
	remoteConn net.Conn
	clientAddr string
	stats      *user.Stats // 请求者流量计数 (nil-safe)
	wsMu       sync.Mutex  // 仅保护 WS 写
	mu         sync.Mutex  // 仅保护 remoteConn 指针赋值
	closed     atomic.Bool
	done       chan struct{}
	closeOnce  sync.Once
}

func newSession(ws *websocket.Conn, clientAddr string, stats *user.Stats) *session {
	return &session{ws: ws, clientAddr: clientAddr, stats: stats, done: make(chan struct{})}
}

func (s *session) cleanup() {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.done)
		s.mu.Lock()
		if s.remoteConn != nil {
			s.remoteConn.Close()
			s.remoteConn = nil
		}
		s.mu.Unlock()
		s.ws.Close()
	})
}

func (s *session) isClosed() bool {
	return s.closed.Load()
}

func (s *session) writeWS(data []byte) error {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	if s.closed.Load() {
		return fmt.Errorf("session closed")
	}
	// 慢客户端最多拖住本次写 writeTimeout, 不会永久占住 wsMu
	_ = s.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	err := s.ws.WriteMessage(websocket.BinaryMessage, data)
	_ = s.ws.SetWriteDeadline(time.Time{})
	return err
}

// setRemote 仅在 dial 成功后调用一次
func (s *session) setRemote(conn net.Conn) {
	s.mu.Lock()
	s.remoteConn = conn
	s.mu.Unlock()
}

func (s *session) startPingLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			if s.closed.Load() {
				return
			}
			// 只拿 wsMu 写 control, 不阻塞 remote 数据路径;
			// control 写超时 5s, 失败直接退出由 cleanup 回收
			s.wsMu.Lock()
			err := s.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			s.wsMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) handleTCPSession(session *session, targetAddr string, payload []byte) {
	ws := session.ws

	// 连接目标服务器 (TCP), 复用 dialer
	conn, err := tcpDialer.DialContext(context.Background(), "tcp", targetAddr)
	if err != nil {
		s.learnFromDialError(targetAddr, err)
		log.Printf("[ERROR] Failed to connect to %s: %v", targetAddr, err)
		return
	}
	session.setRemote(conn)

	// 发送 VLESS 响应头
	if err := session.writeWS([]byte{vlessVersion, 0}); err != nil {
		log.Printf("[ERROR] Failed to send VLESS response: %v", err)
		return
	}

	// 发送初始 payload (无锁直接写, 此时转发 goroutine 还没启动)
	if len(payload) > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(dialTimeout))
		if n, err := conn.Write(payload); err != nil {
			log.Printf("[ERROR] Failed to write payload to %s: %v", targetAddr, err)
			return
		} else {
			session.stats.AddUp(n)
		}
		_ = conn.SetWriteDeadline(time.Time{})
	}

	// 双向数据转发: 各用 1 个 goroutine, conn 由闭包直引不再经 session 锁
	done := make(chan struct{})
	var closeOnce sync.Once
	closeDone := func() { closeOnce.Do(func() { close(done) }) }

	// Remote -> WebSocket (池化 buffer)
	go func() {
		buf := tcpBufPool.Get().([]byte)
		defer tcpBufPool.Put(buf)
		for {
			n, err := conn.Read(buf)
			if err != nil || session.isClosed() {
				closeDone()
				return
			}
			if n == 0 {
				continue
			}
			session.stats.AddDown(n)
			if err := session.writeWS(buf[:n]); err != nil {
				closeDone()
				return
			}
		}
	}()

	// WebSocket -> Remote (gorilla 每次 ReadMessage 已分配, 直接写 conn 无锁)
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				closeDone()
				return
			}
			ws.SetReadDeadline(time.Now().Add(wsReadDeadline))
			if len(data) == 0 {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if n, err := conn.Write(data); err != nil {
				closeDone()
				return
			} else {
				session.stats.AddUp(n)
			}
		}
	}()

	<-done
}

// handleUDPSession 处理 VLESS UDP 会话 (command=2)
// UDP 包格式 (Xray/VLESS 标准): [2B length BE][payload] 重复拼接
// WS -> Remote: 每个 WS BinaryMessage 内含 1~N 个 [len+data] 包
// Remote -> WS: 每个 UDP datagram 回包为 [2B len BE][data] 的 WS BinaryMessage
func (s *Server) handleUDPSession(session *session, targetAddr string, initialPayload []byte) {
	ws := session.ws

	conn, err := udpDialer.DialContext(context.Background(), "udp", targetAddr)
	if err != nil {
		s.learnFromDialError(targetAddr, err)
		log.Printf("[ERROR] Failed to dial UDP %s: %v", targetAddr, err)
		return
	}
	session.setRemote(conn)

	// 发送 VLESS 响应头
	if err := session.writeWS([]byte{vlessVersion, 0}); err != nil {
		log.Printf("[ERROR] Failed to send VLESS UDP response: %v", err)
		return
	}

	// 转发首包中的 UDP 数据 (单线程, 无锁直写 conn)
	if len(initialPayload) > 0 {
		forwardUDPPayload(conn, session, initialPayload)
	}

	done := make(chan struct{})
	var closeOnce sync.Once
	closeDone := func() { closeOnce.Do(func() { close(done) }) }

	// Remote(UDP) -> WebSocket: 零拷贝, 读入 buf[2:] 回包 buf[:2+n]
	go func() {
		buf := udpBufPool.Get().([]byte)
		defer udpBufPool.Put(buf)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(idleTimeout))
			n, err := conn.Read(buf[2:])
			if err != nil || session.isClosed() {
				// idle 超时是正常回收, 不打日志; 其他错误给 WARN
				if err != nil {
					if nerr, ok := err.(net.Error); !ok || !nerr.Timeout() {
						log.Printf("[WARN] UDP read from %s ended: %v", targetAddr, err)
					}
				}
				closeDone()
				return
			}
			if n == 0 || n+2 > len(buf) {
				continue
			}
			session.stats.AddDown(n)
			binary.BigEndian.PutUint16(buf[:2], uint16(n))
			if err := session.writeWS(buf[:2+n]); err != nil {
				closeDone()
				return
			}
		}
	}()

	// WebSocket -> Remote(UDP): 解析 [len+data] 逐包写入
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				closeDone()
				return
			}
			ws.SetReadDeadline(time.Now().Add(wsReadDeadline))
			if len(data) == 0 {
				continue
			}
			forwardUDPPayload(conn, session, data)
		}
	}()

	<-done
}

// forwardUDPPayload 解析 [2B len][data] 拼接包并逐个写入 UDP 远端
// conn 直写无锁 (调用方保证单写者: 首包串行 + WS 循环单 goroutine)
func forwardUDPPayload(conn net.Conn, session *session, data []byte) {
	offset := 0
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	for {
		if offset+2 > len(data) {
			if offset != len(data) {
				log.Printf("[WARN] Truncated UDP length header: %d bytes left", len(data)-offset)
			}
			break // 截断包静默丢弃, 高并发下不打日志
		}
		pktLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		offset += 2
		if pktLen == 0 {
			continue
		}
		if offset+pktLen > len(data) {
			log.Printf("[WARN] Truncated UDP packet: need %d, have %d", pktLen, len(data)-offset)
			break
		}
		if session.isClosed() {
			break
		}
		if n, err := conn.Write(data[offset : offset+pktLen]); err != nil {
			break
		} else {
			session.stats.AddUp(n)
		}
		offset += pktLen
		if offset >= len(data) {
			break
		}
	}
}
