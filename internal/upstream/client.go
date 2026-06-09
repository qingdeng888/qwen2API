package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/qingdeng888/qwen2API/internal/config"
	"github.com/qingdeng888/qwen2API/internal/pool"
)

const BaseURL = "https://chat.qwen.ai"

type ChatIDPoolInterface interface {
	Acquire(email, model string) string
}

type QwenClient struct {
	accountPool *pool.AccountPool
	cfg         *config.Settings
	httpClient  *http.Client
	chatIDPool  ChatIDPoolInterface
}

func NewQwenClient(accountPool *pool.AccountPool, cfg *config.Settings) *QwenClient {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     30 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:   true,
	}
	return &QwenClient{
		accountPool: accountPool,
		cfg:         cfg,
		httpClient:  &http.Client{Transport: transport, Timeout: 300 * time.Second},
	}
}

func (c *QwenClient) SetChatIDPool(p ChatIDPoolInterface) { c.chatIDPool = p }
func (c *QwenClient) Close()                              { c.httpClient.CloseIdleConnections() }
func (c *QwenClient) Pool() *pool.AccountPool             { return c.accountPool }
func (c *QwenClient) Cfg() *config.Settings               { return c.cfg }

func (c *QwenClient) headers(token string) map[string]string {
	return map[string]string{
		"Authorization":  "Bearer " + token,
		"User-Agent":     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Accept":         "application/json, text/plain, */*",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
		"Referer":        BaseURL + "/",
		"Origin":         BaseURL,
		"Content-Type":   "application/json",
	}
}

func (c *QwenClient) RequestJSON(method, path, token string, body interface{}, timeout time.Duration) (int, string, error) {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, BaseURL+path, bodyReader)
	if err != nil {
		return 0, "", err
	}
	for k, v := range c.headers(token) {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(respBody), nil
}

func (c *QwenClient) CreateChat(token, model, chatType string, usePrewarmed bool) (string, error) {
	if usePrewarmed && c.chatIDPool != nil {
		if acc := c.accountPool.FindByToken(token); acc != nil {
			if cached := c.chatIDPool.Acquire(acc.Email, model); cached != "" {
				log.Printf("[上游] 预热池命中 邮箱=%s 会话=%s", acc.Email, cached)
				return cached, nil
			}
		}
	}
	ts := time.Now().Unix()
	body := map[string]interface{}{
		"title": fmt.Sprintf("api_%d", ts), "models": []string{model},
		"chat_mode": "normal", "chat_type": chatType, "timestamp": ts,
	}
	status, respBody, err := c.RequestJSON("POST", "/api/v2/chats/new", token, body, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("create_chat: %w", err)
	}
	if status != 200 {
		if status == 401 || status == 403 {
			return "", fmt.Errorf("unauthorized: HTTP %d", status)
		}
		if status == 429 {
			return "", fmt.Errorf("429 Too Many Requests")
		}
		return "", fmt.Errorf("create_chat HTTP %d: %s", status, trunc(respBody, 100))
	}
	var result struct {
		Success bool `json:"success"`
		Data    struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(respBody), &result); err != nil || !result.Success || result.Data.ID == "" {
		return "", fmt.Errorf("create_chat failed: %s", trunc(respBody, 200))
	}
	return result.Data.ID, nil
}

func (c *QwenClient) DeleteChat(token, chatID string) error {
	if token == "" || chatID == "" {
		return nil
	}
	status, body, err := c.RequestJSON("DELETE", "/api/v2/chats/"+chatID, token, nil, 20*time.Second)
	if err != nil {
		return err
	}
	if status == 200 || status == 204 || status == 404 {
		return nil
	}
	return fmt.Errorf("delete_chat HTTP %d: %s", status, trunc(body, 200))
}

func (c *QwenClient) DeleteChatReliable(token, chatID string) {
	for i := 0; i < c.cfg.ChatDeleteRetryAttempts; i++ {
		if c.DeleteChat(token, chatID) == nil {
			return
		}
		time.Sleep(time.Duration(c.cfg.ChatDeleteRetryDelaySeconds*1000) * time.Millisecond)
	}
}

func (c *QwenClient) StreamChat(ctx context.Context, token, chatID, model, content string, opts StreamOptions) (<-chan StreamEvent, error) {
	payload := BuildChatPayload(chatID, model, content, opts)
	data, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", BaseURL+"/api/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	for k, v := range c.headers(token) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, trunc(string(body), 200))
	}

	ch := make(chan StreamEvent, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		ConsumeSSE(resp.Body, ch)
	}()
	return ch, nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
