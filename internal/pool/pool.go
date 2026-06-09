package pool

import (
	"log"
	"sync"
	"time"

	"github.com/qingdeng888/qwen2API/internal/config"
	"github.com/qingdeng888/qwen2API/internal/database"
)

type AccountPool struct {
	mu       sync.Mutex
	accounts []*Account
	db       *database.JsonDB
	cfg      *config.Settings
	waitCh   chan struct{}
}

func NewAccountPool(db *database.JsonDB, cfg *config.Settings) *AccountPool {
	return &AccountPool{db: db, cfg: cfg, waitCh: make(chan struct{}, 1)}
}

func (p *AccountPool) Load() {
	p.mu.Lock()
	defer p.mu.Unlock()

	data := p.db.GetList()
	p.accounts = make([]*Account, 0, len(data))
	for _, item := range data {
		if m, ok := item.(map[string]interface{}); ok {
			acc := NewAccountFromMap(m)
			if acc.Token != "" {
				p.accounts = append(p.accounts, acc)
			}
		}
	}

	envAccounts := config.LoadEnvAccounts()
	existing := make(map[string]struct{})
	for _, acc := range p.accounts {
		existing[acc.Token] = struct{}{}
	}
	for _, ea := range envAccounts {
		if _, exists := existing[ea["token"]]; !exists {
			p.accounts = append(p.accounts, NewAccountFromEnv(ea))
		}
	}
	log.Printf("[账号池] 加载 %d 个账号", len(p.accounts))
}

func (p *AccountPool) Accounts() []*Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]*Account, len(p.accounts))
	copy(cp, p.accounts)
	return cp
}

func (p *AccountPool) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.accounts)
}

func (p *AccountPool) Acquire(exclude map[string]struct{}) *Account {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := float64(time.Now().Unix())
	var best *Account
	for _, acc := range p.accounts {
		if exclude != nil {
			if _, ex := exclude[acc.Email]; ex {
				continue
			}
		}
		if !acc.Valid || acc.RateLimitedUntil > now {
			continue
		}
		if acc.Inflight >= p.cfg.MaxInflightPerAccount {
			continue
		}
		if best == nil || acc.Inflight < best.Inflight ||
			(acc.Inflight == best.Inflight && acc.LastUsed < best.LastUsed) {
			best = acc
		}
	}
	if best != nil {
		best.mu.Lock()
		best.Inflight++
		best.LastUsed = now
		best.LastRequestStarted = now
		best.mu.Unlock()
	}
	return best
}

func (p *AccountPool) AcquireWait(timeout time.Duration, exclude map[string]struct{}) *Account {
	deadline := time.Now().Add(timeout)
	for {
		if acc := p.Acquire(exclude); acc != nil {
			return acc
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-p.waitCh:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (p *AccountPool) Release(acc *Account) {
	if acc == nil {
		return
	}
	acc.mu.Lock()
	acc.Inflight--
	if acc.Inflight < 0 {
		acc.Inflight = 0
	}
	acc.LastRequestFinished = float64(time.Now().Unix())
	acc.mu.Unlock()
	select {
	case p.waitCh <- struct{}{}:
	default:
	}
}

func (p *AccountPool) MarkRateLimited(acc *Account) {
	acc.mu.Lock()
	defer acc.mu.Unlock()
	acc.RateLimitStrikes++
	cd := p.cfg.RateLimitBaseCooldown
	for i := 1; i < acc.RateLimitStrikes && cd < p.cfg.RateLimitMaxCooldown; i++ {
		cd *= 2
	}
	if cd > p.cfg.RateLimitMaxCooldown {
		cd = p.cfg.RateLimitMaxCooldown
	}
	acc.RateLimitedUntil = float64(time.Now().Unix()) + float64(cd)
	log.Printf("[账号池] 限流 email=%s cooldown=%ds", acc.Email, cd)
}

func (p *AccountPool) MarkInvalid(acc *Account) {
	acc.mu.Lock()
	defer acc.mu.Unlock()
	acc.Valid = false
	acc.StatusCode = "auth_error"
	log.Printf("[账号池] 失效 email=%s", acc.Email)
}

func (p *AccountPool) FindByToken(token string) *Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, acc := range p.accounts {
		if acc.Token == token {
			return acc
		}
	}
	return nil
}

func (p *AccountPool) FindByEmail(email string) *Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, acc := range p.accounts {
		if acc.Email == email {
			return acc
		}
	}
	return nil
}

func (p *AccountPool) RemoveByEmail(email string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, acc := range p.accounts {
		if acc.Email == email {
			p.accounts = append(p.accounts[:i], p.accounts[i+1:]...)
			return true
		}
	}
	return false
}

func (p *AccountPool) AddAccount(acc *Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts = append(p.accounts, acc)
}

func (p *AccountPool) SaveToDB() error {
	p.mu.Lock()
	list := make([]interface{}, 0)
	for _, acc := range p.accounts {
		if acc.Source != "env" {
			list = append(list, acc.ToMap())
		}
	}
	p.mu.Unlock()
	return p.db.Save(list)
}
