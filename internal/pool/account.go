package pool

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/zhangguoguo1314/mimo-free-api/internal/config"
	"github.com/zhangguoguo1314/mimo-free-api/internal/mimo"
)

type Pool struct {
	clients  []*entry
	counter  atomic.Uint64
	mu       sync.RWMutex
}

type entry struct {
	account config.Account
	client  *mimo.WebClient
	healthy bool
}

func New(accounts []config.Account) *Pool {
	p := &Pool{}
	for _, acc := range accounts {
		if !acc.Active {
			continue
		}
		p.clients = append(p.clients, &entry{
			account: acc,
			client:  mimo.NewWebClient(acc.ServiceToken, acc.UserID, acc.Ph),
			healthy: true,
		})
	}
	return p
}

// Next 获取下一个可用客户端
func (p *Pool) Next() (*mimo.WebClient, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.clients) == 0 {
		return nil, fmt.Errorf("no accounts configured")
	}
	n := len(p.clients)
	for i := 0; i < n; i++ {
		idx := int(p.counter.Add(1)) % n
		if p.clients[idx].healthy {
			return p.clients[idx].client, nil
		}
	}
	return p.clients[0].client, nil
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
			account: acc,
			client:  mimo.NewWebClient(acc.ServiceToken, acc.UserID, acc.Ph),
			healthy: true,
		})
	}
}

func (p *Pool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.clients)
}

func (p *Pool) HealthCheck(ctx context.Context) map[string]bool {
	// 先读取账号列表（读锁），再逐个验证（不持锁），最后更新状态（写锁）
	type entry struct {
		id      string
		client  *mimo.WebClient
	}
	var entries []entry
	p.mu.RLock()
	for _, e := range p.clients {
		entries = append(entries, entry{id: e.account.ID, client: e.client})
	}
	p.mu.RUnlock()

	results := make(map[string]bool)
	for _, ent := range entries {
		err := ent.client.Validate(ctx)
		results[ent.id] = err == nil
	}

	// 更新健康状态
	p.mu.Lock()
	for _, e := range p.clients {
		if healthy, ok := results[e.account.ID]; ok {
			e.healthy = healthy
		}
	}
	p.mu.Unlock()

	return results
}
