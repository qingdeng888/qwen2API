package services

import (
	"log"
	"sync"
	"time"

	"github.com/qingdeng888/qwen2API/internal/config"
	"github.com/qingdeng888/qwen2API/internal/pool"
	"github.com/qingdeng888/qwen2API/internal/upstream"
)

// chatIDEntry holds a prewarmed chat ID with creation time
type chatIDEntry struct {
	id        string
	model     string
	createdAt time.Time
}

// ChatIDPool pre-warms chat IDs to reduce latency (500ms~6s per request).
// Replicates Python version behavior:
// - Per-account queues with TTL
// - Consume + immediate refill
// - Periodic refill loop (30s)
// - Expired entries are pruned and upstream chats deleted
// - Flush on account errors
type ChatIDPool struct {
	mu     sync.Mutex
	client *upstream.QwenClient
	pool_  *pool.AccountPool
	cfg    *config.Settings
	queues map[string][]chatIDEntry // email -> queue of prewarmed chat IDs
	stopCh chan struct{}

	// Track which accounts are currently being refilled to avoid duplicates
	refilling map[string]bool
}

func NewChatIDPool(client *upstream.QwenClient, ap *pool.AccountPool, cfg *config.Settings) *ChatIDPool {
	return &ChatIDPool{
		client:    client,
		pool_:     ap,
		cfg:       cfg,
		queues:    make(map[string][]chatIDEntry),
		stopCh:    make(chan struct{}),
		refilling: make(map[string]bool),
	}
}

// Acquire gets a prewarmed chat ID for the given email and model.
// If no matching model found, evicts one entry, creates a new chat for the
// requested model synchronously, and returns it. Returns "" only on failure.
func (p *ChatIDPool) Acquire(email, model string) string {
	p.mu.Lock()
	entries := p.queues[email]
	ttl := time.Duration(p.cfg.ChatIDPrewarmTTLSeconds) * time.Second
	now := time.Now()

	var selected string
	var expiredIDs []string
	var remaining []chatIDEntry

	for _, e := range entries {
		if now.Sub(e.createdAt) >= ttl {
			expiredIDs = append(expiredIDs, e.id)
			continue
		}
		if selected == "" && e.model == model {
			selected = e.id
			log.Printf("[预热池] 命中 email=%s chat_id=%s model=%s age=%ds", email, selected, model, int(now.Sub(e.createdAt).Seconds()))
		} else {
			remaining = append(remaining, e)
		}
	}
	p.queues[email] = remaining
	p.mu.Unlock()

	// Delete expired entries in background
	if len(expiredIDs) > 0 {
		go func() {
			acc := p.pool_.FindByEmail(email)
			if acc == nil {
				return
			}
			for _, id := range expiredIDs {
				p.client.DeleteChatReliable(acc.Token, id)
			}
		}()
	}

	// Model matched — refill in background and return
	if selected != "" {
		go p.refillAccount(email, model, "consume")
		return selected
	}

	// No matching model — evict one and create for the requested model
	p.mu.Lock()
	var evictID string
	if len(p.queues[email]) > 0 {
		last := len(p.queues[email]) - 1
		evictID = p.queues[email][last].id
		p.queues[email] = p.queues[email][:last]
	}
	p.mu.Unlock()

	if evictID != "" {
		go func() {
			acc := p.pool_.FindByEmail(email)
			if acc != nil {
				p.client.DeleteChatReliable(acc.Token, evictID)
			}
		}()
	}

	// Create chat for the requested model synchronously
	acc := p.pool_.FindByEmail(email)
	if acc == nil || acc.Token == "" {
		return ""
	}
	chatID, err := p.client.CreateChat(acc.Token, model, "t2t", false)
	if err != nil {
		log.Printf("[预热池] 动态创建失败 email=%s model=%s err=%v", email, model, err)
		return ""
	}
	log.Printf("[预热池] 动态创建 email=%s model=%s chat_id=%s (淘汰旧条目)", email, model, chatID)

	// Refill with this model in background
	go p.refillAccount(email, model, "dynamic")
	return chatID
}

// Invalidate removes a specific chat_id from the pool (e.g. after error)
func (p *ChatIDPool) Invalidate(email, chatID string) {
	if email == "" || chatID == "" {
		return
	}
	p.mu.Lock()
	entries := p.queues[email]
	var kept []chatIDEntry
	removed := false
	for _, e := range entries {
		if e.id == chatID {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	p.queues[email] = kept
	p.mu.Unlock()

	if removed {
		log.Printf("[预热池] 标记无效 email=%s chat_id=%s", email, chatID)
		go func() {
			acc := p.pool_.FindByEmail(email)
			if acc != nil {
				p.client.DeleteChatReliable(acc.Token, chatID)
			}
		}()
	}
}

// FlushAccount clears all prewarmed chat IDs for an account (after errors)
func (p *ChatIDPool) FlushAccount(email string) int {
	p.mu.Lock()
	entries := p.queues[email]
	p.queues[email] = nil
	p.mu.Unlock()

	if len(entries) == 0 {
		return 0
	}

	log.Printf("[预热池] 清空账号 email=%s count=%d", email, len(entries))
	go func() {
		acc := p.pool_.FindByEmail(email)
		if acc == nil {
			return
		}
		for _, e := range entries {
			p.client.DeleteChatReliable(acc.Token, e.id)
		}
	}()
	return len(entries)
}

// Size returns the pool size for an account
func (p *ChatIDPool) Size(email string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queues[email])
}

// TotalSize returns total prewarmed chat IDs across all accounts
func (p *ChatIDPool) TotalSize() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := 0
	for _, q := range p.queues {
		total += len(q)
	}
	return total
}

// PerAccountSizes returns map of email -> pool size
func (p *ChatIDPool) PerAccountSizes() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make(map[string]int, len(p.queues))
	for email, q := range p.queues {
		if len(q) > 0 {
			result[email] = len(q)
		}
	}
	return result
}

// Start begins the background refill loop
func (p *ChatIDPool) Start() {
	log.Printf("[预热池] 启动 target=%d ttl=%ds max_concurrency=%d",
		p.cfg.ChatIDPrewarmTargetPerAccount, p.cfg.ChatIDPrewarmTTLSeconds, p.cfg.ChatIDPrewarmMaxConcurrency)

	// Initial warmup after 1 second
	time.Sleep(1 * time.Second)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Run first refill immediately
	p.refillAll()

	for {
		select {
		case <-p.stopCh:
			p.flushAll()
			return
		case <-ticker.C:
			p.pruneExpired()
			p.refillAll()
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

// refillAll iterates all valid accounts and refills any below target
func (p *ChatIDPool) refillAll() {
	accounts := p.pool_.Accounts()
	if len(accounts) == 0 {
		return
	}

	defaultModel := config.GetDefaultModel()
	sem := make(chan struct{}, p.cfg.ChatIDPrewarmMaxConcurrency)
	var wg sync.WaitGroup

	for _, acc := range accounts {
		if !acc.Valid || acc.IsRateLimited() || acc.Token == "" {
			continue
		}

		p.mu.Lock()
		qSize := len(p.queues[acc.Email])
		deficit := p.cfg.ChatIDPrewarmTargetPerAccount - qSize
		p.mu.Unlock()

		if deficit <= 0 {
			continue
		}

		// Only create 1 per account per cycle to avoid burst
		wg.Add(1)
		sem <- struct{}{}
		go func(a *pool.Account) {
			defer wg.Done()
			defer func() { <-sem }()
			p.prewarmOne(a, defaultModel)
		}(acc)
	}
	wg.Wait()
}

// refillAccount refills a specific account's pool
func (p *ChatIDPool) refillAccount(email, model, reason string) {
	p.mu.Lock()
	if p.refilling[email] {
		p.mu.Unlock()
		return
	}
	qSize := len(p.queues[email])
	if qSize >= p.cfg.ChatIDPrewarmTargetPerAccount {
		p.mu.Unlock()
		return
	}
	p.refilling[email] = true
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.refilling, email)
		p.mu.Unlock()
	}()

	acc := p.pool_.FindByEmail(email)
	if acc == nil || !acc.Valid || acc.Token == "" {
		return
	}

	useModel := model
	if useModel == "" {
		useModel = config.GetDefaultModel()
	}
	p.prewarmOne(acc, useModel)
}

// prewarmOne creates a single chat_id and adds to pool
func (p *ChatIDPool) prewarmOne(acc *pool.Account, model string) {
	chatID, err := p.client.CreateChat(acc.Token, model, "t2t", false)
	if err != nil {
		log.Printf("[预热池] 预热失败 email=%s err=%v", acc.Email, err)
		return
	}

	p.mu.Lock()
	q := p.queues[acc.Email]
	if len(q) >= p.cfg.ChatIDPrewarmTargetPerAccount {
		// Overfill — discard
		p.mu.Unlock()
		go p.client.DeleteChatReliable(acc.Token, chatID)
		return
	}
	p.queues[acc.Email] = append(q, chatIDEntry{id: chatID, model: model, createdAt: time.Now()})
	newSize := len(p.queues[acc.Email])
	p.mu.Unlock()

	log.Printf("[预热池] 预热成功 email=%s chat_id=%s pool_size=%d", acc.Email, chatID, newSize)
}

// pruneExpired removes expired entries from all queues
func (p *ChatIDPool) pruneExpired() {
	ttl := time.Duration(p.cfg.ChatIDPrewarmTTLSeconds) * time.Second
	now := time.Now()
	var toDelete []struct {
		email  string
		chatID string
	}

	p.mu.Lock()
	for email, entries := range p.queues {
		var kept []chatIDEntry
		for _, e := range entries {
			if now.Sub(e.createdAt) >= ttl {
				toDelete = append(toDelete, struct {
					email  string
					chatID string
				}{email, e.id})
			} else {
				kept = append(kept, e)
			}
		}
		p.queues[email] = kept
	}
	p.mu.Unlock()

	if len(toDelete) > 0 {
		log.Printf("[预热池] 清理过期条目 count=%d ttl=%ds", len(toDelete), int(ttl.Seconds()))
		go func() {
			for _, item := range toDelete {
				acc := p.pool_.FindByEmail(item.email)
				if acc != nil {
					p.client.DeleteChatReliable(acc.Token, item.chatID)
				}
			}
		}()
	}
}

// flushAll removes all entries (called on shutdown)
func (p *ChatIDPool) flushAll() {
	p.mu.Lock()
	var all []struct {
		email  string
		chatID string
		token  string
	}
	for email, entries := range p.queues {
		acc := p.pool_.FindByEmail(email)
		token := ""
		if acc != nil {
			token = acc.Token
		}
		for _, e := range entries {
			all = append(all, struct {
				email  string
				chatID string
				token  string
			}{email, e.id, token})
		}
	}
	p.queues = make(map[string][]chatIDEntry)
	p.mu.Unlock()

	if len(all) > 0 {
		log.Printf("[预热池] 关闭清理 count=%d", len(all))
		for _, item := range all {
			if item.token != "" {
				p.client.DeleteChatReliable(item.token, item.chatID)
			}
		}
	}
}
