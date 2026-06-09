package upstream

import (
	"context"
	"fmt"
	"log"
	"regexp"
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

// Toxic refusal patterns — model refuses to use tools
var toxicRefusalRe = regexp.MustCompile(`(?i)(tool[_\s]*(does not|doesn'?t)\s*exist|I cannot help|I'?m sorry.*I can'?t|I do not have access to|无法使用该工具|工具不存在)`)

func isToxicRefusal(content string) bool {
	return toxicRefusalRe.MatchString(content)
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
				el := strings.ToLower(lastErr)
				if strings.Contains(el, "429") || strings.Contains(el, "rate") {
					e.accountPool.MarkRateLimited(acc)
				} else if strings.Contains(el, "unauthorized") || strings.Contains(el, "401") {
					e.accountPool.MarkInvalid(acc)
				}
				exclude[acc.Email] = struct{}{}
				e.accountPool.Release(acc)
				continue
			}

			// Collect events and check for toxic refusals / empty responses
			var events []StreamEvent
			var totalContent strings.Builder
			hasError := false

			for evt := range streamCh {
				events = append(events, evt)
				if evt.Type == "error" {
					hasError = true
					lastErr = evt.Content
				} else if evt.Content != "" {
					totalContent.WriteString(evt.Content)
				}
			}

			e.activeMu.Lock()
			delete(e.activeIDs, chatID)
			e.activeMu.Unlock()
			go e.client.DeleteChatReliable(acc.Token, chatID)
			e.accountPool.Release(acc)

			// Check for retry conditions
			fullText := totalContent.String()

			// Empty response — retry with different account
			if !hasError && len(fullText) == 0 && len(events) == 0 {
				log.Printf("[上游] 空响应 重试 email=%s attempt=%d", acc.Email, attempt+1)
				lastErr = "empty response"
				exclude[acc.Email] = struct{}{}
				continue
			}

			// Toxic refusal — retry with different account
			if isToxicRefusal(fullText) {
				log.Printf("[上游] 检测到模型拒绝 重试 email=%s content=%s", acc.Email, fullText[:min(len(fullText), 80)])
				lastErr = "toxic refusal: " + fullText[:min(len(fullText), 80)]
				exclude[acc.Email] = struct{}{}
				continue
			}

			// Upstream error in events — check if retryable
			if hasError && len(fullText) == 0 {
				el := strings.ToLower(lastErr)
				if strings.Contains(el, "429") || strings.Contains(el, "rate") {
					e.accountPool.MarkRateLimited(acc)
				}
				exclude[acc.Email] = struct{}{}
				log.Printf("[上游] 流错误 重试 email=%s err=%s", acc.Email, lastErr)
				continue
			}

			// Success — forward all events to client
			for _, evt := range events {
				ch <- ExecutorEvent{Event: &evt}
			}
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
