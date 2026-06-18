package pool

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhangguoguo1314/mimo-free-api/internal/config"
	"github.com/zhangguoguo1314/mimo-free-api/internal/mimo"
)

// PoolConfig 账号池保护配置
type PoolConfig struct {
	MaxConcurrent int           // 单账号最大并发数（默认 2）
	CooldownTime time.Duration // 请求失败后冷却时间（默认 60s）
	RateLimit    int           // 单账号每分钟最大请求数（默认 20）
}

// DefaultPoolConfig 默认保护配置
var DefaultPoolConfig = PoolConfig{
	MaxConcurrent: 2,
	CooldownTime:  60 * time.Second,
	RateLimit:     20,
}

type Pool struct {
	clients  []*entry
	counter  atomic.Uint64
	mu       sync.RWMutex
	cfg      PoolConfig
}

type entry struct {
	account    config.Account
	client     *mimo.WebClient
	healthy    bool
	active     int32         // 当前正在处理的请求数（原子操作）
	cooldownAt atomic.Int64 // 冷却截止时间（unix nano，0 表示无冷却）
	timestamps []int64      // 滑动窗口时间戳，用于速率限制
}

func New(accounts []config.Account) *Pool {
	return NewWithConfig(accounts, DefaultPoolConfig)
}

func NewWithConfig(accounts []config.Account, cfg PoolConfig) *Pool {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = DefaultPoolConfig.MaxConcurrent
	}
	if cfg.CooldownTime <= 0 {
		cfg.CooldownTime = DefaultPoolConfig.CooldownTime
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = DefaultPoolConfig.RateLimit
	}

	p := &Pool{cfg: cfg}
	for _, acc := range accounts {
		if !acc.Active {
			continue
		}
		p.clients = append(p.clients, &entry{
			account:    acc,
			client:     mimo.NewWebClient(acc.ServiceToken, acc.UserID, acc.Ph),
			healthy:    true,
			timestamps: make([]int64, 0, cfg.RateLimit),
		})
	}
	return p
}

// Acquire 获取一个可用客户端，并标记为使用中。返回的 ReleaseFunc 必须在请求完成后调用。
func (p *Pool) Acquire() (*mimo.WebClient, func(), error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.clients) == 0 {
		return nil, nil, fmt.Errorf("no accounts configured")
	}

	now := time.Now()
	n := len(p.clients)

	// 第一轮：找健康、未冷却、未超并发、未超速率的账号
	for i := 0; i < n; i++ {
		idx := int(p.counter.Add(1)) % n
		e := p.clients[idx]
		if p.isAvailable(e, now) {
			e.active++ // 原子递增
			e.addTimestamp(now)
			return e.client, func() { p.release(e) }, nil
		}
	}

	// 第二轮：如果所有账号都繁忙，找并发最低的（允许超限分配）
	var best *entry
	bestActive := int32(999999)
	for _, e := range p.clients {
		if !e.healthy {
			continue
		}
		a := atomic.LoadInt32(&e.active)
		if a < bestActive {
			bestActive = a
			best = e
		}
	}
	if best != nil {
		best.active++
		best.addTimestamp(now)
		return best.client, func() { p.release(best) }, nil
	}

	// 兜底：使用第一个账号
	e := p.clients[0]
	e.active++
	e.addTimestamp(now)
	return e.client, func() { p.release(e) }, nil
}

// release 释放账号占用
func (p *Pool) release(e *entry) {
	atomic.AddInt32(&e.active, -1)
}

// isAvailable 检查账号是否可用
func (p *Pool) isAvailable(e *entry, now time.Time) bool {
	// 健康检查
	if !e.healthy {
		return false
	}

	// 冷却检查
	cd := e.cooldownAt.Load()
	if cd > 0 && now.UnixNano() < cd {
		return false
	}

	// 并发检查
	if atomic.LoadInt32(&e.active) >= int32(p.cfg.MaxConcurrent) {
		return false
	}

	// 速率检查（滑动窗口：最近 60 秒内的请求数）
	if len(e.timestamps) >= p.cfg.RateLimit {
		cutoff := now.Add(-60 * time.Second).UnixNano()
		// 清理过期时间戳
		j := 0
		for _, ts := range e.timestamps {
			if ts >= cutoff {
				e.timestamps[j] = ts
				j++
			}
		}
		e.timestamps = e.timestamps[:j]
		if len(e.timestamps) >= p.cfg.RateLimit {
			return false
		}
	}

	return true
}

// addTimestamp 记录请求时间戳
func (e *entry) addTimestamp(now time.Time) {
	cutoff := now.Add(-60 * time.Second).UnixNano()
	j := 0
	for _, ts := range e.timestamps {
		if ts >= cutoff {
			e.timestamps[j] = ts
			j++
		}
	}
	e.timestamps = e.timestamps[:j]
	e.timestamps = append(e.timestamps, now.UnixNano())
}

// MarkCooldown 将账号标记为冷却状态
func (p *Pool) MarkCooldown(client *mimo.WebClient) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, e := range p.clients {
		if e.client == client {
			e.cooldownAt.Store(time.Now().Add(p.cfg.CooldownTime).UnixNano())
			return
		}
	}
}

// MarkUnhealthy 将账号标记为不健康
func (p *Pool) MarkUnhealthy(client *mimo.WebClient) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, e := range p.clients {
		if e.client == client {
			e.healthy = false
			return
		}
	}
}

// Next 获取下一个可用客户端（向后兼容，不追踪占用）
func (p *Pool) Next() (*mimo.WebClient, error) {
	client, _, err := p.Acquire()
	return client, err
}

// Status 返回所有账号的状态信息
func (p *Pool) Status() []AccountStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	statuses := make([]AccountStatus, 0, len(p.clients))
	for _, e := range p.clients {
		cd := e.cooldownAt.Load()
		cooldownRemaining := time.Duration(0)
		if cd > 0 && now.UnixNano() < cd {
			cooldownRemaining = time.Duration(cd - now.UnixNano())
		}
		statuses = append(statuses, AccountStatus{
			ID:               e.account.ID,
			Healthy:          e.healthy,
			ActiveRequests:   atomic.LoadInt32(&e.active),
			CooldownRemaining: cooldownRemaining,
			RateUsed:         len(e.timestamps),
			RateLimit:        p.cfg.RateLimit,
			MaxConcurrent:    p.cfg.MaxConcurrent,
		})
	}
	return statuses
}

// AccountStatus 账号状态信息
type AccountStatus struct {
	ID                string
	Healthy           bool
	ActiveRequests    int32
	CooldownRemaining time.Duration
	RateUsed          int
	RateLimit         int
	MaxConcurrent     int
}

func (p *Pool) HasAccounts() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.clients) > 0
}

func (p *Pool) Reload(accounts []config.Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clients = nil
	for _, acc := range accounts {
		if !acc.Active {
			continue
		}
		p.clients = append(p.clients, &entry{
			account:    acc,
			client:     mimo.NewWebClient(acc.ServiceToken, acc.UserID, acc.Ph),
			healthy:    true,
			timestamps: make([]int64, 0, p.cfg.RateLimit),
		})
	}
}

func (p *Pool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.clients)
}

func (p *Pool) HealthCheck(ctx context.Context) map[string]bool {
	type hcEntry struct {
		id     string
		client *mimo.WebClient
	}
	var entries []hcEntry
	p.mu.RLock()
	for _, e := range p.clients {
		entries = append(entries, hcEntry{id: e.account.ID, client: e.client})
	}
	p.mu.RUnlock()

	results := make(map[string]bool)
	for _, ent := range entries {
		err := ent.client.Validate(ctx)
		results[ent.id] = err == nil
	}

	p.mu.Lock()
	for _, e := range p.clients {
		if healthy, ok := results[e.account.ID]; ok {
			e.healthy = healthy
			// 如果健康检查通过，清除冷却状态
			if healthy {
				e.cooldownAt.Store(0)
			}
		}
	}
	p.mu.Unlock()

	return results
}
