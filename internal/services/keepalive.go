package services

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/qingdeng888/qwen2API/internal/config"
	"github.com/qingdeng888/qwen2API/internal/database"
)

type KeepAliveService struct {
	configDB *database.JsonDB
	cfg      *config.Settings
	stopCh   chan struct{}
	client   *http.Client
}

func NewKeepAliveService(configDB *database.JsonDB, cfg *config.Settings) *KeepAliveService {
	return &KeepAliveService{configDB: configDB, cfg: cfg, stopCh: make(chan struct{}), client: &http.Client{Timeout: 10 * time.Second}}
}

func (s *KeepAliveService) Start() {
	url, interval := s.getConfig()
	if url == "" {
		return
	}
	log.Printf("[Keepalive] url=%s interval=%ds", url, interval)
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			url, _ = s.getConfig()
			if url == "" {
				continue
			}
			resp, err := s.client.Get(url)
			if err == nil {
				resp.Body.Close()
			}
		}
	}
}

func (s *KeepAliveService) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

func (s *KeepAliveService) getConfig() (string, int) {
	url := os.Getenv("KEEPALIVE_URL")
	if url == "" {
		data := s.configDB.GetMap()
		url, _ = data["keepalive_url"].(string)
	}
	interval := 60
	if v := os.Getenv("KEEPALIVE_INTERVAL"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			interval = i
		}
	} else {
		data := s.configDB.GetMap()
		if i, ok := data["keepalive_interval"].(float64); ok {
			interval = int(i)
		}
	}
	if interval < 5 {
		interval = 5
	}
	if interval > 86400 {
		interval = 86400
	}
	return url, interval
}
