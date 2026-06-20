package pool

import (
	"context"
	"fmt"
	"math/rand"
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
	DailyLimit   int           // 单账号每天最大请求数（默认 150）
	JitterMax    time.Duration // Acquire 成功后随机抖动延迟上限（默认 800ms）
}

// DefaultPoolConfig 默认保护配置
var DefaultPoolConfig = PoolConfig{
	MaxConcurrent: 2,
	CooldownTime:  60 * time.Second,
	RateLimit:     20,
	DailyLimit:    150,
	JitterMax:     800 * time.Millisecond,
}

// weightedEntry 加权选择用的候选条目
type weightedEntry struct {
	e      *entry
	weight int
}

type Pool struct {
	clients  []*entry
	counter  uint64
	mu       sync.RWMutex
	cfg      PoolConfig
}

type entry struct {
	account       config.Account
	client        *mimo.WebClient
	healthy       bool
	active        int32         // 当前正在处理的请求数（原子操作）
	cooldownAt    int64         // 冷却截止时间（unix nano，0 表示无冷却）
	dailyCount    int32         // 当天已使用次数（原子操作）
	dailyResetAt  int64         // 日用量重置时间（unix nano，UTC+8 0 点）
	fail429Count  int32         // 连续 429 错误计数（原子操作）
	tsMu          sync.Mutex    // 保护 timestamps 的互斥锁
	timestamps    []int64       // 滑动窗口时间戳，用于速率限制
}

// imax returns the maximum of two ints.
func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
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
	if cfg.DailyLimit <= 0 {
		cfg.DailyLimit = DefaultPoolConfig.DailyLimit
	}
	if cfg.JitterMax <= 0 {
		cfg.JitterMax = DefaultPoolConfig.JitterMax
	}

	p := &Pool{cfg: cfg}
	resetAt := nextDailyReset()
	for _, acc := range accounts {
		if !acc.Active {
			continue
		}
		e := &entry{
			account:      acc,
			client:       mimo.NewWebClient(acc.ServiceToken, acc.UserID, acc.Ph),
			healthy:      true,
			timestamps:   make([]int64, 0, cfg.RateLimit),
		}
		atomic.StoreInt64(&e.dailyResetAt, resetAt)
		p.clients = append(p.clients, e)
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

	// 第一轮：加权随机选择（healthy 且未冷却的账号才有权重）
	var candidates []weightedEntry
	for _, e := range p.clients {
		if !e.healthy {
			continue
		}
		cd := atomic.LoadInt64(&e.cooldownAt)
		if cd > 0 && now.UnixNano() < cd {
			continue
		}
		a := atomic.LoadInt32(&e.active)
		dc := atomic.LoadInt32(&e.dailyCount)
		// 检查日用量是否超限
		if dc >= int32(p.cfg.DailyLimit) {
			continue
		}
		// 检查并发是否超限
		if a >= int32(p.cfg.MaxConcurrent) {
			continue
		}
		// 检查速率是否超限
		if !p.checkRateLimit(e, now) {
			continue
		}
		// 计算权重
		w := imax(1, int(int32(p.cfg.MaxConcurrent)-a)) *
			imax(1, int(int32(p.cfg.RateLimit)-int32(p.rateUsed(e, now)))) *
			imax(1, int(int32(p.cfg.DailyLimit)-dc))
		candidates = append(candidates, weightedEntry{e: e, weight: w})
	}

	if len(candidates) > 0 {
		e := weightedRandomSelect(candidates)
		// 日用量重置检查
		p.resetDailyIfNeeded(e, now)
		atomic.AddInt32(&e.dailyCount, 1)
		atomic.AddInt32(&e.active, 1)
		e.addTimestamp(now)
		// 请求抖动：0 ~ JitterMax 随机延迟
		if p.cfg.JitterMax > 0 {
			jitter := time.Duration(rand.Int63n(int64(p.cfg.JitterMax)))
			time.Sleep(jitter)
		}
		return e.client, func() { p.release(e) }, nil
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
		p.resetDailyIfNeeded(best, now)
		atomic.AddInt32(&best.dailyCount, 1)
		atomic.AddInt32(&best.active, 1)
		best.addTimestamp(now)
		if p.cfg.JitterMax > 0 {
			jitter := time.Duration(rand.Int63n(int64(p.cfg.JitterMax)))
			time.Sleep(jitter)
		}
		return best.client, func() { p.release(best) }, nil
	}

	// 兜底：使用第一个账号
	e := p.clients[0]
	p.resetDailyIfNeeded(e, now)
	atomic.AddInt32(&e.dailyCount, 1)
	atomic.AddInt32(&e.active, 1)
	e.addTimestamp(now)
	if p.cfg.JitterMax > 0 {
		jitter := time.Duration(rand.Int63n(int64(p.cfg.JitterMax)))
		time.Sleep(jitter)
	}
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
	cd := atomic.LoadInt64(&e.cooldownAt)
	if cd > 0 && now.UnixNano() < cd {
		return false
	}

	// 并发检查
	if atomic.LoadInt32(&e.active) >= int32(p.cfg.MaxConcurrent) {
		return false
	}

	// 日用量检查
	p.resetDailyIfNeeded(e, now)
	if atomic.LoadInt32(&e.dailyCount) >= int32(p.cfg.DailyLimit) {
		return false
	}

	// 速率检查（滑动窗口：最近 60 秒内的请求数）
	if !p.checkRateLimit(e, now) {
		return false
	}

	return true
}

// checkRateLimit 检查速率限制（不持有 tsMu 锁的简化版本，用于加权选择预判）
func (p *Pool) checkRateLimit(e *entry, now time.Time) bool {
	e.tsMu.Lock()
	defer e.tsMu.Unlock()
	if len(e.timestamps) >= p.cfg.RateLimit {
		cutoff := now.Add(-60 * time.Second).UnixNano()
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

// rateUsed 返回当前滑动窗口内的请求数（调用者需确保线程安全或仅在估算场景使用）
func (p *Pool) rateUsed(e *entry, now time.Time) int {
	e.tsMu.Lock()
	defer e.tsMu.Unlock()
	cutoff := now.Add(-60 * time.Second).UnixNano()
	j := 0
	for _, ts := range e.timestamps {
		if ts >= cutoff {
			e.timestamps[j] = ts
			j++
		}
	}
	e.timestamps = e.timestamps[:j]
	return len(e.timestamps)
}

// resetDailyIfNeeded 检查是否需要重置日用量（基于 UTC+8 的日期变化）
func (p *Pool) resetDailyIfNeeded(e *entry, now time.Time) {
	resetAt := atomic.LoadInt64(&e.dailyResetAt)
	if now.UnixNano() >= resetAt {
		newResetAt := nextDailyReset()
		atomic.StoreInt64(&e.dailyResetAt, newResetAt)
		atomic.StoreInt32(&e.dailyCount, 0)
	}
}

// nextDailyReset 计算下一个 UTC+8 0 点的 unix nano 时间戳
func nextDailyReset() int64 {
	// 获取当前 UTC+8 时间
	loc := time.FixedZone("CST", 8*3600)
	now := time.Now().In(loc)
	// 下一个 0 点
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc)
	return next.UnixNano()
}

// weightedRandomSelect 根据权重随机选择一个 entry
func weightedRandomSelect(candidates []weightedEntry) *entry {
	totalWeight := 0
	for _, c := range candidates {
		totalWeight += c.weight
	}
	if totalWeight <= 0 {
		return candidates[0].e
	}
	r := rand.Intn(totalWeight)
	for _, c := range candidates {
		r -= c.weight
		if r < 0 {
			return c.e
		}
	}
	return candidates[len(candidates)-1].e
}

// addTimestamp 记录请求时间戳
func (e *entry) addTimestamp(now time.Time) {
	e.tsMu.Lock()
	defer e.tsMu.Unlock()
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
			atomic.StoreInt64(&e.cooldownAt, time.Now().Add(p.cfg.CooldownTime).UnixNano())
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

// MarkRateLimit 429 专用：递增 fail429Count，超过 3 次标记不健康并冷却
func (p *Pool) MarkRateLimit(client *mimo.WebClient) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, e := range p.clients {
		if e.client == client {
			count := atomic.AddInt32(&e.fail429Count, 1)
			if count >= 3 {
				e.healthy = false
				atomic.StoreInt64(&e.cooldownAt, time.Now().Add(p.cfg.CooldownTime).UnixNano())
			} else {
				// 未达到阈值，仅冷却
				atomic.StoreInt64(&e.cooldownAt, time.Now().Add(p.cfg.CooldownTime).UnixNano())
			}
			return
		}
	}
}

// MarkAuthFailed 401 专用：标记不健康（Cookie 失效不自动恢复）
func (p *Pool) MarkAuthFailed(client *mimo.WebClient) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, e := range p.clients {
		if e.client == client {
			e.healthy = false
			return
		}
	}
}

// MarkTempError 502/503 专用：仅冷却 30 秒，不标记不健康
func (p *Pool) MarkTempError(client *mimo.WebClient) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, e := range p.clients {
		if e.client == client {
			atomic.StoreInt64(&e.cooldownAt, time.Now().Add(30 * time.Second).UnixNano())
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
		cd := atomic.LoadInt64(&e.cooldownAt)
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
			DailyUsed:        int(atomic.LoadInt32(&e.dailyCount)),
			DailyLimit:       p.cfg.DailyLimit,
			Fail429Count:     atomic.LoadInt32(&e.fail429Count),
			Source:           e.account.Source,
			AddedAt:          formatTimestamp(e.account.AddedAt),
		})
	}
	return statuses
}

// AccountStatus 账号状态信息
type AccountStatus struct {
	ID                string        `json:"id"`
	Healthy           bool          `json:"healthy"`
	ActiveRequests    int32         `json:"active"`
	CooldownRemaining time.Duration `json:"cooldown_remaining"`
	RateUsed          int           `json:"rate_used"`
	RateLimit         int           `json:"rate_limit"`
	MaxConcurrent     int           `json:"max_concurrent"`
	DailyUsed         int           `json:"daily_used"`
	DailyLimit        int           `json:"daily_limit"`
	Fail429Count      int32         `json:"fail429_count"`
	Source            string        `json:"source"`
	AddedAt           string        `json:"added_at"`
}

func (p *Pool) HasAccounts() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.clients) > 0
}

func (p *Pool) Reload(accounts []config.Account) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 构建新账号的 ID -> Account 映射
	newMap := make(map[string]config.Account)
	for _, acc := range accounts {
		if acc.Active {
			newMap[acc.ID] = acc
		}
	}

	// 构建旧账号的 ID -> entry 映射
	oldMap := make(map[string]*entry)
	for _, e := range p.clients {
		oldMap[e.account.ID] = e
	}

	// 计算新增、删除、变更
	var newClients []*entry
	resetAt := nextDailyReset()
	for id, acc := range newMap {
		if old, exists := oldMap[id]; exists {
			// 已存在：检查是否有变更（比较关键字段）
			if old.account.ServiceToken == acc.ServiceToken &&
				old.account.UserID == acc.UserID &&
				old.account.Ph == acc.Ph {
				// 无变更，保留原有 entry（包括 WebClient 和状态）
				old.account = acc // 更新元数据（Source, AddedAt 等）
				newClients = append(newClients, old)
			} else {
				// 有变更：创建新 entry
				e := &entry{
					account:    acc,
					client:     mimo.NewWebClient(acc.ServiceToken, acc.UserID, acc.Ph),
					healthy:    true,
					timestamps: make([]int64, 0, p.cfg.RateLimit),
				}
				atomic.StoreInt64(&e.dailyResetAt, resetAt)
				newClients = append(newClients, e)
			}
		} else {
			// 新增账号
			e := &entry{
				account:    acc,
				client:     mimo.NewWebClient(acc.ServiceToken, acc.UserID, acc.Ph),
				healthy:    true,
				timestamps: make([]int64, 0, p.cfg.RateLimit),
			}
			atomic.StoreInt64(&e.dailyResetAt, resetAt)
			newClients = append(newClients, e)
		}
	}
	// 已删除的账号不在 newMap 中，自动被丢弃

	p.clients = newClients
}

// GetClientByID 根据 ID 获取 WebClient
func (p *Pool) GetClientByID(id string) *mimo.WebClient {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, e := range p.clients {
		if e.account.ID == id {
			return e.client
		}
	}
	return nil
}

// formatTimestamp 将 unix nano 时间戳格式化为可读字符串
func formatTimestamp(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(0, ts).Format("2006-01-02 15:04:05")
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
			// 如果健康检查通过，清除冷却状态并重置 429 计数
			if healthy {
				atomic.StoreInt64(&e.cooldownAt, 0)
				atomic.StoreInt32(&e.fail429Count, 0)
			}
		}
	}
	p.mu.Unlock()

	return results
}
