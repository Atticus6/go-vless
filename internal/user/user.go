// Package user 多用户注册表: 以 UUID 区分用户, 每个用户独立累计上/下行流量.
// 计数为进程内存态, 重启清零.
package user

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

// Stats 单用户流量计数 (字节).
// up: 客户端经 WS 发往远端的字节; down: 远端经 WS 回给客户端的字节.
type Stats struct {
	up   atomic.Uint64
	down atomic.Uint64
}

// AddUp 累加上行字节 (nil-safe).
func (s *Stats) AddUp(n int) {
	if s == nil || n <= 0 {
		return
	}
	s.up.Add(uint64(n))
}

// AddDown 累加下行字节 (nil-safe).
func (s *Stats) AddDown(n int) {
	if s == nil || n <= 0 {
		return
	}
	s.down.Add(uint64(n))
}

// Snapshot 累计值快照.
func (s *Stats) Snapshot() (up, down uint64) {
	if s == nil {
		return 0, 0
	}
	return s.up.Load(), s.down.Load()
}

// UserTraffic 单用户流量快照 (JSON 友好).
type UserTraffic struct {
	Up   uint64 `json:"up"`
	Down uint64 `json:"down"`
}

// SnapshotAll 全用户流量快照, key 为 UUID 字符串 (管理视角).
func (r *Registry) SnapshotAll() map[string]UserTraffic {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]UserTraffic, len(r.users))
	for id, st := range r.users {
		up, down := st.Snapshot()
		out[id.String()] = UserTraffic{Up: up, Down: down}
	}
	return out
}

// Registry 用户注册表, 并发安全.
type Registry struct {
	mu    sync.RWMutex
	users map[uuid.UUID]*Stats
}

// New 用一组 UUID 建表 (去重, 空表合法但谁也认证不过).
func New(ids []uuid.UUID) *Registry {
	r := &Registry{users: make(map[uuid.UUID]*Stats, len(ids))}
	for _, id := range ids {
		if _, ok := r.users[id]; !ok {
			r.users[id] = &Stats{}
		}
	}
	return r
}

// Valid 该 UUID 是否为合法用户.
func (r *Registry) Valid(id uuid.UUID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.users[id]
	return ok
}

// StatsFor 取用户计数器, 未知用户返回 nil.
// 注意: 即使之后 Remove 掉该用户, 已拿到的指针仍可安全计数 (GC 保障),
// 只是不再可见; 新会话因 Valid 失败被拒.
func (r *Registry) StatsFor(id uuid.UUID) *Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.users[id]
}

// Add 新增用户 (已存在则幂等, 保留已有流量).
func (r *Registry) Add(id uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.users[id]; !ok {
		r.users[id] = &Stats{}
	}
}

// Remove 删除用户, 返回之前是否存在.
// 进行中的会话不受影响 (继续计数到孤儿 Stats, 内存安全); 新会话直接被拒.
func (r *Registry) Remove(id uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.users[id]; !ok {
		return false
	}
	delete(r.users, id)
	return true
}

// Update 批量增删, 单次写锁内原子生效 (先删后加, 同一 UUID 出现在两边时以 add 为准).
func (r *Registry) Update(add, remove []uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range remove {
		delete(r.users, id)
	}
	for _, id := range add {
		if _, ok := r.users[id]; !ok {
			r.users[id] = &Stats{}
		}
	}
}

// List 用户列表快照.
func (r *Registry) List() []uuid.UUID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]uuid.UUID, 0, len(r.users))
	for id := range r.users {
		out = append(out, id)
	}
	return out
}

// ParseList 解析逗号分隔的 UUID 串 (flag/env), 去重去空, 无有效项报错.
func ParseList(s string) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	seen := make(map[uuid.UUID]struct{})
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := uuid.Parse(part)
		if err != nil {
			return nil, fmt.Errorf("invalid UUID %q: %w", part, err)
		}
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no valid UUID")
	}
	return ids, nil
}
