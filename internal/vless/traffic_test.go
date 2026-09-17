package vless

import (
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/atticus6/go-vless/internal/user"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// buildHeader 组最小 VLESS 请求头 (IPv4) + 可选初始 payload
func buildHeader(id uuid.UUID, cmd byte, ip net.IP, port int, payload []byte) []byte {
	h := []byte{vlessVersion}
	raw, _ := id.MarshalBinary()
	h = append(h, raw...)
	h = append(h, 0x00) // addon len
	h = append(h, cmd)
	var pb [2]byte
	binary.BigEndian.PutUint16(pb[:], uint16(port))
	h = append(h, pb[:]...)
	h = append(h, atypIPv4)
	h = append(h, ip.To4()...)
	return append(h, payload...)
}

func startTCPReflect(t *testing.T) (addr string, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if _, err := c.Write(buf[:n]); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func startUDPReflect(t *testing.T) (addr string, closeFn func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, raddr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := pc.WriteTo(buf[:n], raddr); err != nil {
				return
			}
		}
	}()
	return pc.LocalAddr().String(), func() { pc.Close() }
}

func dialVLESS(t *testing.T, wsURL string, header []byte) *websocket.Conn {
	t.Helper()
	d, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := d.WriteMessage(websocket.BinaryMessage, header); err != nil {
		t.Fatal(err)
	}
	// VLESS 响应头 2 字节
	_, resp, err := d.ReadMessage()
	if err != nil || len(resp) != 2 {
		t.Fatalf("VLESS response = %v, %v; want 2 bytes", resp, err)
	}
	return d
}

func TestTCPSessionTraffic(t *testing.T) {
	id := uuid.New()
	users := user.New([]uuid.UUID{id})
	srv := New(users, 100)
	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.Handler)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()
	wsURL := "ws://" + httpSrv.Listener.Addr().String() + "/"

	echoAddr, closeEcho := startTCPReflect(t)
	defer closeEcho()
	host, portStr, _ := net.SplitHostPort(echoAddr)
	portNum := 0
	for _, c := range portStr {
		portNum = portNum*10 + int(c-'0')
	}

	d := dialVLESS(t, wsURL, buildHeader(id, cmdTCP, net.ParseIP(host), portNum, []byte("ping")))
	defer d.Close()

	if err := d.WriteMessage(websocket.BinaryMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	// TCP 流会粘包/拆包, 累积读够 9 字节为止 (ping+hello, 保序)
	var got []byte
	_ = d.SetReadDeadline(time.Now().Add(5 * time.Second))
	for len(got) < 9 {
		_, chunk, err := d.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, chunk...)
	}
	if string(got) != "pinghello" {
		t.Errorf("echo = %q, want %q", got, "pinghello")
	}

	up, down := users.StatsFor(id).Snapshot()
	t.Logf("up=%d down=%d", up, down)
	if up != 9 {
		t.Errorf("up = %d, want 9 (ping+hello)", up)
	}
	if down != 9 {
		t.Errorf("down = %d, want 9 (echoes)", down)
	}
}

func TestUDPSessionTraffic(t *testing.T) {
	id := uuid.New()
	users := user.New([]uuid.UUID{id})
	srv := New(users, 100)
	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.Handler)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()
	wsURL := "ws://" + httpSrv.Listener.Addr().String() + "/"

	echoAddr, closeEcho := startUDPReflect(t)
	defer closeEcho()
	host, portStr, _ := net.SplitHostPort(echoAddr)
	portNum := 0
	for _, c := range portStr {
		portNum = portNum*10 + int(c-'0')
	}

	d := dialVLESS(t, wsURL, buildHeader(id, cmdUDP, net.ParseIP(host), portNum, nil))
	defer d.Close()

	// [len BE][data]
	pkt := []byte{0x00, 0x04, 'p', 'i', 'n', 'g'}
	if err := d.WriteMessage(websocket.BinaryMessage, pkt); err != nil {
		t.Fatal(err)
	}
	_, echo, err := d.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if len(echo) != 6 || string(echo[2:]) != "ping" {
		t.Fatalf("udp echo = %q, want [len]+ping", echo)
	}

	up, down := users.StatsFor(id).Snapshot()
	if up != 4 {
		t.Errorf("up = %d, want 4", up)
	}
	if down != 4 {
		t.Errorf("down = %d, want 4", down)
	}
}

func TestUnknownUUIDRejected(t *testing.T) {
	id := uuid.New()
	users := user.New([]uuid.UUID{id})
	srv := New(users, 100)
	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.Handler)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()
	wsURL := "ws://" + httpSrv.Listener.Addr().String() + "/"

	d, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_ = d.SetReadDeadline(time.Now().Add(5 * time.Second))
	// 未知 UUID 的首包
	if err := d.WriteMessage(websocket.BinaryMessage, buildHeader(uuid.New(), cmdTCP, net.ParseIP("127.0.0.1"), 80, nil)); err != nil {
		t.Fatal(err)
	}
	// 服务端鉴权失败直接关连接, 读应失败且无 2 字节响应头
	_, resp, err := d.ReadMessage()
	if err == nil {
		t.Fatalf("want closed conn, got response %q", resp)
	}
}
