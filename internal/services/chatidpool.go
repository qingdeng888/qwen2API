package services

import (
	"log"
	"sync"
	"time"

	"github.com/qingdeng888/qwen2API/internal/config"
	"github.com/qingdeng888/qwen2API/internal/pool"
	"github.com/qingdeng888/qwen2API/internal/upstream"
)

type chatIDEntry struct {
	id        string
	createdAt time.Time
}

type ChatIDPool struct {
	mu     sync.Mutex
	client *upstream.QwenClient
	pool_  *pool.AccountPool
	cfg    *config.Settings
	cache  map[string][]chatIDEntry
	stopCh chan struct{}
}

func NewChatIDPool(client *upstream.QwenClient, ap *pool.AccountPool, cfg *config.Settings) *ChatIDPool {
	return &ChatIDPool{client: client, pool_: ap, cfg: cfg, cache: make(map[string][]chatIDEntry), stopCh: make(chan struct{})}
}

func (p *ChatIDPool) Acquire(email, model string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := p.cache[email]
	ttl := time.Duration(p.cfg.ChatIDPrewarmTTLSeconds) * time.Second
	now := time.Now()
	for i, e := range entries {
		if now.Sub(e.createdAt) < ttl {
			p.cache[email] = append(entries[:i], entries[i+1:]...)
			return e.id
		}
	}
	p.cache[email] = nil
	return ""
}

func (p *ChatIDPool) Start() {
	log.Printf("[预热池] 启动 target=%d ttl=%ds", p.cfg.ChatIDPrewarmTargetPerAccount, p.cfg.ChatIDPrewarmTTLSeconds)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.prewarm()
		}
	}
}

func (p *ChatIDPool) Stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
}

func (p *ChatIDPool) prewarm() {
	accounts := p.pool_.Accounts()
	sem := make(chan struct{}, p.cfg.ChatIDPrewarmMaxConcurrency)
	var wg sync.WaitGroup
	for _, acc := range accounts {
		if !acc.Valid || acc.IsRateLimited() {
			continue
		}
		p.mu.Lock()
		needed := p.cfg.ChatIDPrewarmTargetPerAccount - len(p.cache[acc.Email])
		p.mu.Unlock()
		if needed <= 0 {
			continue
		}
		for i := 0; i < needed; i++ {
			wg.Add(1)
			sem <- struct{}{}
			go func(a *pool.Account) {
				defer wg.Done()
				defer func() { <-sem }()
				id, err := p.client.CreateChat(a.Token, "qwen3.6-plus", "t2t", false)
				if err != nil {
					return
				}
				p.mu.Lock()
				p.cache[a.Email] = append(p.cache[a.Email], chatIDEntry{id: id, createdAt: time.Now()})
				p.mu.Unlock()
			}(acc)
		}
	}
	wg.Wait()
}
