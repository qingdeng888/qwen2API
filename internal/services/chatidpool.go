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
	model     string
	createdAt time.Time
}

type ChatIDPool struct {
	mu     sync.Mutex
	client *upstream.QwenClient
	pool_  *pool.AccountPool
	cfg    *config.Settings
	cache  map[string][]chatIDEntry // email -> list of prewarmed chat IDs
	stopCh chan struct{}
}

func NewChatIDPool(client *upstream.QwenClient, ap *pool.AccountPool, cfg *config.Settings) *ChatIDPool {
	return &ChatIDPool{client: client, pool_: ap, cfg: cfg, cache: make(map[string][]chatIDEntry), stopCh: make(chan struct{})}
}

// Acquire gets a prewarmed chat ID for the given email and model
func (p *ChatIDPool) Acquire(email, model string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := p.cache[email]
	ttl := time.Duration(p.cfg.ChatIDPrewarmTTLSeconds) * time.Second
	now := time.Now()

	for i, e := range entries {
		// Must match model AND not be expired
		if e.model == model && now.Sub(e.createdAt) < ttl {
			p.cache[email] = append(entries[:i], entries[i+1:]...)
			return e.id
		}
	}

	// Clean expired entries
	valid := entries[:0]
	for _, e := range entries {
		if now.Sub(e.createdAt) < ttl {
			valid = append(valid, e)
		}
	}
	p.cache[email] = valid
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
	if len(accounts) == 0 {
		return
	}

	// Prewarm the default model for each account
	defaultModel := config.GetDefaultModel()
	sem := make(chan struct{}, p.cfg.ChatIDPrewarmMaxConcurrency)
	var wg sync.WaitGroup

	for _, acc := range accounts {
		if !acc.Valid || acc.IsRateLimited() {
			continue
		}
		p.mu.Lock()
		// Count entries for this model specifically
		count := 0
		for _, e := range p.cache[acc.Email] {
			if e.model == defaultModel {
				count++
			}
		}
		needed := p.cfg.ChatIDPrewarmTargetPerAccount - count
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
				id, err := p.client.CreateChat(a.Token, defaultModel, "t2t", false)
				if err != nil {
					return
				}
				p.mu.Lock()
				p.cache[a.Email] = append(p.cache[a.Email], chatIDEntry{id: id, model: defaultModel, createdAt: time.Now()})
				p.mu.Unlock()
			}(acc)
		}
	}
	wg.Wait()
}
