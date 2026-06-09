package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/qingdeng888/qwen2API/internal/api"
	"github.com/qingdeng888/qwen2API/internal/config"
	"github.com/qingdeng888/qwen2API/internal/database"
	"github.com/qingdeng888/qwen2API/internal/pool"
	"github.com/qingdeng888/qwen2API/internal/services"
	"github.com/qingdeng888/qwen2API/internal/upstream"
)

func main() {
	// Load .env if exists
	loadDotEnv()

	cfg := config.Load()

	log.Printf("[启动] qwen2API v%s Enterprise Gateway (Go)", config.VERSION)
	log.Printf("[配置] PORT=%d MAX_INFLIGHT=%d PREWARM_TARGET=%d LOG=%s",
		cfg.Port, cfg.MaxInflightPerAccount, cfg.ChatIDPrewarmTargetPerAccount, cfg.LogLevel)

	// Initialize databases
	accountsDB := database.NewJsonDB(cfg.AccountsFile, database.DefaultList)
	usersDB := database.NewJsonDB(cfg.UsersFile, database.DefaultList)
	capturesDB := database.NewJsonDB(cfg.CapturesFile, database.DefaultList)
	configDB := database.NewJsonDB(cfg.ConfigFile, database.DefaultMap)
	uploadedFilesDB := database.NewJsonDB(cfg.UploadedFilesFile, database.DefaultList)

	accountsDB.Load()
	usersDB.Load()
	capturesDB.Load()
	configDB.Load()
	uploadedFilesDB.Load()

	// Initialize account pool
	accountPool := pool.NewAccountPool(accountsDB, cfg)
	accountPool.Load()

	// Initialize upstream client
	qwenClient := upstream.NewQwenClient(accountPool, cfg)

	// Chat ID prewarm pool
	chatIDPool := services.NewChatIDPool(qwenClient, accountPool, cfg)
	qwenClient.SetChatIDPool(chatIDPool)
	go chatIDPool.Start()

	// Keepalive service
	keepalive := services.NewKeepAliveService(configDB, cfg)
	go keepalive.Start()

	// Setup routes
	mux := http.NewServeMux()
	appCtx := &api.AppContext{
		Config:          cfg,
		AccountsDB:      accountsDB,
		UsersDB:         usersDB,
		CapturesDB:      capturesDB,
		ConfigDB:        configDB,
		UploadedFilesDB: uploadedFilesDB,
		AccountPool:     accountPool,
		QwenClient:      qwenClient,
		ChatIDPool:      chatIDPool,
	}
	api.RegisterRoutes(mux, appCtx)

	// Wrap with SPA static file handler (serves frontend/dist with fallback to index.html)
	handler := api.NewSPAHandler(mux, "frontend/dist")

	// Start server
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      handler,
		ReadTimeout:  300 * time.Second,
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("[启动] HTTP 服务监听 :%d", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[致命] %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[关闭] 正在优雅关闭...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	chatIDPool.Stop()
	keepalive.Stop()
	qwenClient.Close()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("[关闭] 异常: %v", err)
	}
	log.Println("[关闭] 服务已停止")
}

// loadDotEnv loads .env file (simple implementation, no external deps)
func loadDotEnv() {
	data, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for _, line := range splitLines(string(data)) {
		line = trimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		idx := indexOf(line, '=')
		if idx < 0 {
			continue
		}
		key := trimSpace(line[:idx])
		val := trimSpace(line[idx+1:])
		// Remove surrounding quotes
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
