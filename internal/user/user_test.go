package user

import (
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestParseList(t *testing.T) {
	a := "14725836-1234-5678-9abc-def012345678"
	b := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	ids, err := ParseList(a)
	if err != nil || len(ids) != 1 {
		t.Fatalf("single = %v, %v; want 1 id", ids, err)
	}

	// 多用户 + 空格 + 去重
	ids, err = ParseList(a + ", " + b + "," + a + " ,,")
	if err != nil || len(ids) != 2 {
		t.Fatalf("multi = %v, %v; want 2 ids", ids, err)
	}

	for _, bad := range []string{"", "   ", "not-a-uuid", a + ",zzz"} {
		if _, err := ParseList(bad); err == nil {
			t.Errorf("ParseList(%q) = nil error, want error", bad)
		}
	}
}

func TestRegistry(t *testing.T) {
	a := uuid.MustParse("14725836-1234-5678-9abc-def012345678")
	b := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	unknown := uuid.New()

	r := New([]uuid.UUID{a, b, a})
	if !r.Valid(a) || !r.Valid(b) || r.Valid(unknown) {
		t.Fatalf("Valid mismatch")
	}
	if r.StatsFor(unknown) != nil {
		t.Fatalf("StatsFor(unknown) want nil")
	}

	st := r.StatsFor(a)
	st.AddUp(100)
	st.AddDown(200)
	st.AddUp(-5) // 非正忽略
	if up, down := st.Snapshot(); up != 100 || down != 200 {
		t.Errorf("Snapshot = (%d,%d), want (100,200)", up, down)
	}
	// b 不受影响
	if up, down := r.StatsFor(b).Snapshot(); up != 0 || down != 0 {
		t.Errorf("other user = (%d,%d), want (0,0)", up, down)
	}

	// nil-safe
	var nilStats *Stats
	nilStats.AddUp(1)
	nilStats.AddDown(1)
	if up, down := nilStats.Snapshot(); up != 0 || down != 0 {
		t.Errorf("nil Snapshot = (%d,%d), want (0,0)", up, down)
	}
}

// TestResetAll 上报成功后清零语义：计数归零、用户保留、nil 安全.
func TestResetAll(t *testing.T) {
	a := uuid.MustParse("14725836-1234-5678-9abc-def012345678")
	b := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	r := New([]uuid.UUID{a, b})
	r.StatsFor(a).AddUp(100)
	r.StatsFor(a).AddDown(200)
	r.ResetAll()
	if up, down := r.StatsFor(a).Snapshot(); up != 0 || down != 0 {
		t.Errorf("after ResetAll = (%d,%d), want (0,0)", up, down)
	}
	// 用户保留：清零后仍合法，且可继续累计.
	if !r.Valid(a) || !r.Valid(b) {
		t.Error("ResetAll should not remove users")
	}
	r.StatsFor(a).AddUp(50)
	if up, _ := r.StatsFor(a).Snapshot(); up != 50 {
		t.Errorf("after recount up = %d, want 50", up)
	}
	// ResetUsers 只清指定用户：未知 id 跳过，其他用户不受影响.
	r.StatsFor(a).AddUp(300)
	r.StatsFor(b).AddUp(400)
	r.ResetUsers([]string{"not-a-uuid", a.String()})
	if up, _ := r.StatsFor(a).Snapshot(); up != 0 {
		t.Errorf("ResetUsers(a) up = %d, want 0", up)
	}
	if up, _ := r.StatsFor(b).Snapshot(); up != 400 {
		t.Errorf("ResetUsers should not touch b, up = %d, want 400", up)
	}
	// nil-safe
	var nilReg *Registry
	nilReg.ResetAll()
	nilReg.ResetUsers([]string{a.String()})
	var nilStats *Stats
	nilStats.Reset()
}

// TestRegistryConcurrent 高并发混合读写 (-race 必跑):
// 读写锁 + 原子计数 + 删除后孤儿指针计数都不应触发 race 或 panic.
func TestRegistryConcurrent(t *testing.T) {
	a := uuid.MustParse("14725836-1234-5678-9abc-def012345678")
	b := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	c := uuid.New()
	r := New([]uuid.UUID{a, b})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.Add(c)
				_ = r.Valid(a)
				if st := r.StatsFor(a); st != nil {
					st.AddUp(1)
					st.AddDown(1)
				}
				_ = r.SnapshotAll()
				_ = r.List()
				if (n+j)%3 == 0 {
					r.Remove(c)
				}
				r.Update([]uuid.UUID{c}, nil)
			}
		}(i)
	}
	wg.Wait()

	// 事后一致性: 注册表可用, 计数单调
	r.Add(a)
	up, _ := r.StatsFor(a).Snapshot()
	if up == 0 {
		t.Errorf("up = 0 after concurrent adds, want > 0")
	}
	if !r.Valid(a) {
		t.Errorf("Valid(a) = false after concurrent ops")
	}
}
