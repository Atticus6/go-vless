package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var (
	uuidStr string
	port    int64

	userUUID uuid.UUID
)

func init() {
	// 默认值
	defaultUUID := "147258369-1234-5678-9abc-def012345678"
	defaultPort := int64(3325)

	// 环境变量覆盖默认值
	if envUUID := os.Getenv("UUID"); envUUID != "" {
		defaultUUID = envUUID
	}
	if envPort := os.Getenv("PORT"); envPort != "" {
		if p, err := parseInt64(envPort); err == nil {
			defaultPort = p
		}
	}

	flag.StringVar(&uuidStr, "uuid", defaultUUID, "VLESS UUID (env: UUID)")
	flag.Int64Var(&port, "port", defaultPort, "Server Port (env: PORT)")

}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
}

func main() {
	flag.Parse()

	// 解析 UUID
	var err error
	userUUID, err = uuid.Parse(uuidStr)
	if err != nil {
		log.Fatalf("Invalid UUID: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handler)
	mux.HandleFunc("/health", healthHandler)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 优雅关闭
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		log.Println("Shutting down server...")

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}
	}()

	log.Printf("VLESS Server listening on :%d", port)
	log.Printf("UUID: %s", userUUID.String())
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
	log.Println("Server stopped")
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func handler(w http.ResponseWriter, r *http.Request) {
	upgrade := strings.ToLower(r.Header.Get("Upgrade"))

	if upgrade != "websocket" {
		if r.URL.Path == "/" {
			w.Write([]byte("Bad Request"))
		} else {
			log.Printf("[WARN] Expected WebSocket, got Upgrade: %s", r.Header.Get("Upgrade"))
			http.Error(w, "Expected WebSocket", http.StatusUpgradeRequired)
		}
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ERROR] WebSocket upgrade failed: %v", err)
		return
	}

	log.Printf("[INFO] New connection from %s", r.RemoteAddr)
	handleVLESSSession(ws, r.RemoteAddr)
}

// VLESS 协议常量
const (
	vlessVersion = 0
)

// VLESS 地址类型
const (
	atypIPv4   = 1
	atypDomain = 2
	atypIPv6   = 3
)

// VLESS 命令类型
const (
	cmdTCP = 1
	cmdUDP = 2
	cmdMux = 3
)

func handleVLESSSession(ws *websocket.Conn, clientAddr string) {
	var (
		remoteConn net.Conn
		mu         sync.Mutex
		closed     bool
	)

	cleanup := func() {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		closed = true
		if remoteConn != nil {
			remoteConn.Close()
			remoteConn = nil
		}
		ws.Close()
		log.Printf("[INFO] Connection closed: %s", clientAddr)
	}
	defer cleanup()

	// 设置 ping/pong 保活
	ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// 定期发送 ping
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			<-ticker.C
			mu.Lock()
			if closed {
				mu.Unlock()
				return
			}
			if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				mu.Unlock()
				return
			}
			mu.Unlock()
		}
	}()

	// 读取第一个消息（VLESS 请求头）
	_, headerData, err := ws.ReadMessage()
	if err != nil {
		log.Printf("[ERROR] Failed to read VLESS header: %v", err)
		return
	}

	// 解析 VLESS 请求
	targetAddr, command, payload, err := parseVLESSRequest(headerData)
	if err != nil {
		log.Printf("[ERROR] Invalid VLESS request from %s: %v", clientAddr, err)
		return
	}

	if command != cmdTCP {
		log.Printf("[WARN] Unsupported command: %d", command)
		return
	}

	// 连接目标服务器
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.Dial("tcp", targetAddr)
	if err != nil {
		log.Printf("[ERROR] Failed to connect to %s: %v", targetAddr, err)
		return
	}

	mu.Lock()
	remoteConn = conn
	mu.Unlock()

	log.Printf("[INFO] Connected to remote: %s", targetAddr)

	// 发送 VLESS 响应头
	responseHeader := []byte{vlessVersion, 0} // version + addon length (0)
	mu.Lock()
	err = ws.WriteMessage(websocket.BinaryMessage, responseHeader)
	mu.Unlock()
	if err != nil {
		log.Printf("[ERROR] Failed to send VLESS response: %v", err)
		return
	}

	// 如果有 payload，先发送到目标服务器
	if len(payload) > 0 {
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write(payload); err != nil {
			log.Printf("[ERROR] Failed to write payload: %v", err)
			return
		}
		conn.SetWriteDeadline(time.Time{})
	}

	// 双向数据转发
	done := make(chan struct{})
	var closeOnce sync.Once
	closeDone := func() { closeOnce.Do(func() { close(done) }) }

	// Remote -> WebSocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				closeDone()
				return
			}
			mu.Lock()
			if closed {
				mu.Unlock()
				closeDone()
				return
			}
			err = ws.WriteMessage(websocket.BinaryMessage, buf[:n])
			mu.Unlock()
			if err != nil {
				closeDone()
				return
			}
		}
	}()

	// WebSocket -> Remote
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				closeDone()
				return
			}
			ws.SetReadDeadline(time.Now().Add(60 * time.Second))
			mu.Lock()
			if closed || remoteConn == nil {
				mu.Unlock()
				closeDone()
				return
			}
			_, err = remoteConn.Write(data)
			mu.Unlock()
			if err != nil {
				closeDone()
				return
			}
		}
	}()

	<-done
	log.Printf("[INFO] Session ended: %s -> %s", clientAddr, targetAddr)
}

// parseVLESSRequest 解析 VLESS 请求
// VLESS 协议格式:
// +----------+----------+----------+----------+----------+----------+----------+
// | Version  |  UUID    | Addon    | Command  | Port     | AddrType | Address  |
// | 1 byte   | 16 bytes | Variable | 1 byte   | 2 bytes  | 1 byte   | Variable |
// +----------+----------+----------+----------+----------+----------+----------+
func parseVLESSRequest(data []byte) (addr string, command byte, payload []byte, err error) {
	if len(data) < 24 {
		return "", 0, nil, fmt.Errorf("data too short: %d", len(data))
	}

	// 版本检查
	version := data[0]
	if version != vlessVersion {
		return "", 0, nil, fmt.Errorf("unsupported version: %d", version)
	}

	// UUID 验证
	reqUUID, err := uuid.FromBytes(data[1:17])
	if err != nil {
		return "", 0, nil, fmt.Errorf("invalid UUID: %v", err)
	}
	if reqUUID != userUUID {
		return "", 0, nil, fmt.Errorf("UUID mismatch")
	}

	// Addon 长度
	addonLen := data[17]
	offset := 18 + int(addonLen)

	if len(data) < offset+4 {
		return "", 0, nil, fmt.Errorf("data too short for command")
	}

	// 命令
	command = data[offset]
	offset++

	// 端口 (big-endian)
	port := binary.BigEndian.Uint16(data[offset : offset+2])
	offset += 2

	// 地址类型
	addrType := data[offset]
	offset++

	var host string
	switch addrType {
	case atypIPv4:
		if len(data) < offset+4 {
			return "", 0, nil, fmt.Errorf("data too short for IPv4")
		}
		host = net.IP(data[offset : offset+4]).String()
		offset += 4
	case atypDomain:
		if len(data) < offset+1 {
			return "", 0, nil, fmt.Errorf("data too short for domain length")
		}
		domainLen := int(data[offset])
		offset++
		if len(data) < offset+domainLen {
			return "", 0, nil, fmt.Errorf("data too short for domain")
		}
		host = string(data[offset : offset+domainLen])
		offset += domainLen
	case atypIPv6:
		if len(data) < offset+16 {
			return "", 0, nil, fmt.Errorf("data too short for IPv6")
		}
		host = net.IP(data[offset : offset+16]).String()
		offset += 16
	default:
		return "", 0, nil, fmt.Errorf("unsupported address type: %d", addrType)
	}

	addr = fmt.Sprintf("%s:%d", host, port)

	// 剩余数据作为 payload
	if offset < len(data) {
		payload = data[offset:]
	}

	return addr, command, payload, nil
}
