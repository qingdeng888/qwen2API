package upstream

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/qingdeng888/qwen2API/internal/config"
	"github.com/qingdeng888/qwen2API/internal/pool"
)

type ExecutorMeta struct {
	ChatID  string
	Account *pool.Account
}

type ExecutorEvent struct {
	Meta  *ExecutorMeta
	Event *StreamEvent
	Error error
}

type Executor struct {
	client      *QwenClient
	accountPool *pool.AccountPool
	cfg         *config.Settings
	activeMu    sync.Mutex
	activeIDs   map[string]struct{}
}

func NewExecutor(client *QwenClient, accountPool *pool.AccountPool, cfg *config.Settings) *Executor {
	return &Executor{client: client, accountPool: accountPool, cfg: cfg, activeIDs: make(map[string]struct{})}
}

func (e *Executor) StreamWithRetry(ctx context.Context, model, content string, opts StreamOptions) <-chan ExecutorEvent {
	ch := make(chan ExecutorEvent, 64)
	go func() {
		defer close(ch)
		exclude := make(map[string]struct{})
		var lastErr string

		for attempt := 0; attempt < e.cfg.MaxRetries; attempt++ {
			acc := e.accountPool.AcquireWait(60*time.Second, exclude)
			if acc == nil {
				ch <- ExecutorEvent{Error: fmt.Errorf("no available accounts")}
				return
			}
			log.Printf("[上游] 获取账号 email=%s model=%s 第%d次", acc.Email, model, attempt+1)

			ct := NormalizeChatType(opts.ChatType)
			if ct == "" {
				ct = "t2t"
			}
			chatID, err := e.client.CreateChat(acc.Token, model, ct, true)
			if err != nil {
				lastErr = err.Error()
				el := strings.ToLower(lastErr)
				if strings.Contains(el, "unauthorized") || strings.Contains(el, "401") || strings.Contains(el, "403") {
					e.accountPool.MarkInvalid(acc)
				} else if strings.Contains(el, "429") {
					e.accountPool.MarkRateLimited(acc)
				}
				exclude[acc.Email] = struct{}{}
				e.accountPool.Release(acc)
				continue
			}

			e.activeMu.Lock()
			e.activeIDs[chatID] = struct{}{}
			e.activeMu.Unlock()

			ch <- ExecutorEvent{Meta: &ExecutorMeta{ChatID: chatID, Account: acc}}

			streamCh, err := e.client.StreamChat(ctx, acc.Token, chatID, model, content, opts)
			if err != nil {
				e.activeMu.Lock()
				delete(e.activeIDs, chatID)
				e.activeMu.Unlock()
				go e.client.DeleteChatReliable(acc.Token, chatID)
				lastErr = err.Error()
				exclude[acc.Email] = struct{}{}
				e.accountPool.Release(acc)
				continue
			}

			for evt := range streamCh {
				ch <- ExecutorEvent{Event: &evt}
			}

			e.activeMu.Lock()
			delete(e.activeIDs, chatID)
			e.activeMu.Unlock()
			go e.client.DeleteChatReliable(acc.Token, chatID)
			e.accountPool.Release(acc)
			return
		}
		if lastErr != "" {
			ch <- ExecutorEvent{Error: fmt.Errorf("all %d attempts failed: %s", e.cfg.MaxRetries, lastErr)}
		} else {
			ch <- ExecutorEvent{Error: fmt.Errorf("all %d attempts failed", e.cfg.MaxRetries)}
		}
	}()
	return ch
}
