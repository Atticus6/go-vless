package vless

import (
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/atticus6/go-vless/internal/user"
)

func mkServer(v4, v6 bool) *Server {
	s := &Server{users: user.New(nil), sem: make(chan struct{}, 1), max: 1}
	s.hasIPv4.Store(v4)
	s.hasIPv6.Store(v6)
	return s
}

func TestFamilyAllowedLiteralIP(t *testing.T) {
	v4only := mkServer(true, false)
	v6only := mkServer(false, true)
	dual := mkServer(true, true)
	none := mkServer(false, false)

	cases := []struct {
		name string
		s    *Server
		host string
		want bool
	}{
		{"v4only allows ipv4", v4only, "1.2.3.4", true},
		{"v4only rejects ipv6", v4only, "2001:db8::1", false},
		{"v6only rejects ipv4", v6only, "1.2.3.4", false},
		{"v6only allows ipv6", v6only, "2001:db8::1", true},
		{"dual allows ipv4", dual, "1.2.3.4", true},
		{"dual allows ipv6", dual, "2001:db8::1", true},
		{"none rejects ipv4", none, "1.2.3.4", false},
		{"none rejects ipv6", none, "2001:db8::1", false},
		// IPv4-mapped IPv6 按 IPv4 判
		{"v4only allows mapped", v4only, "::ffff:1.2.3.4", true},
		{"v6only rejects mapped", v6only, "::ffff:1.2.3.4", false},
	}

	for _, c := range cases {
		if got := c.s.familyAllowed(c.host); got != c.want {
			t.Errorf("%s: familyAllowed(%q) = %v, want %v", c.name, c.host, got, c.want)
		}
	}
}

// dialErr 构造带指定 errno 的 dial 错误
func dialErr(errno syscall.Errno) error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: errno}}
}

func TestFamilyDead(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"network unreachable", dialErr(syscall.ENETUNREACH), true},
		{"af not supported", dialErr(syscall.EAFNOSUPPORT), true},
		{"proto not supported", dialErr(syscall.EPROTONOSUPPORT), true},
		{"addr not avail", dialErr(syscall.EADDRNOTAVAIL), true},
		// 目标侧问题一律不算缺族
		{"conn refused", dialErr(syscall.ECONNREFUSED), false},
		{"timed out", dialErr(syscall.ETIMEDOUT), false},
		{"dns error", &net.DNSError{Err: "no such host", IsNotFound: true}, false},
		{"plain error", os.ErrClosed, false},
	}

	for _, c := range cases {
		if got := familyDead(c.err); got != c.want {
			t.Errorf("%s: familyDead = %v, want %v", c.name, got, c.want)
		}
	}

	// 超时 OpError 即使包着 ENETUNREACH 也不算 (黑洞分不清是哪边的问题)
	timeoutErr := &net.OpError{Op: "dial", Net: "tcp", Err: &timeoutError{}}
	if familyDead(timeoutErr) {
		t.Errorf("timeout OpError: familyDead = true, want false")
	}
}

type timeoutError struct{}

func (e *timeoutError) Error() string   { return "i/o timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

func TestLearnFromDialError(t *testing.T) {
	// 字面 IPv6 + ENETUNREACH: 只锁 IPv6, IPv4 不动
	s := mkServer(true, true)
	s.learnFromDialError("[2001:db8::1]:443", dialErr(syscall.ENETUNREACH))
	if s.hasIPv6.Load() {
		t.Errorf("hasIPv6 = true, want false after ENETUNREACH on literal IPv6")
	}
	if !s.hasIPv4.Load() {
		t.Errorf("hasIPv4 = false, want true (unrelated family untouched)")
	}
	if s.familyAllowed("2001:db8::2") {
		t.Errorf("familyAllowed(ipv6) = true after latch, want false")
	}

	// 连接拒绝不锁存
	s2 := mkServer(true, true)
	s2.learnFromDialError("[2001:db8::1]:443", dialErr(syscall.ECONNREFUSED))
	if !s2.hasIPv6.Load() {
		t.Errorf("hasIPv6 latched on ECONNREFUSED, want untouched")
	}

	// 域名目标无法归因, 不锁存
	s3 := mkServer(true, true)
	s3.learnFromDialError("example.com:443", dialErr(syscall.ENETUNREACH))
	if !s3.hasIPv6.Load() || !s3.hasIPv4.Load() {
		t.Errorf("domain target latched flags, want untouched")
	}
}
