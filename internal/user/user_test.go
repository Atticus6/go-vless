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
