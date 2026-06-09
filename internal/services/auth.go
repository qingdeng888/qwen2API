package services

import (
	"net/http"
	"strings"

	"github.com/qingdeng888/qwen2API/internal/config"
	"github.com/qingdeng888/qwen2API/internal/database"
)

type AuthContext struct {
	Token string
	User  map[string]interface{}
}

func ExtractAPIToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		if t := strings.TrimSpace(auth[7:]); t != "" {
			return t
		}
	}
	if k := strings.TrimSpace(r.Header.Get("x-api-key")); k != "" {
		return k
	}
	if k := r.URL.Query().Get("key"); k != "" {
		return strings.TrimSpace(k)
	}
	if k := r.URL.Query().Get("api_key"); k != "" {
		return strings.TrimSpace(k)
	}
	return ""
}

func ResolveAuth(r *http.Request, usersDB *database.JsonDB, cfg *config.Settings) (*AuthContext, int, string) {
	token := ExtractAPIToken(r)
	if token == "" {
		return nil, 401, "Invalid API Key"
	}
	keys := config.GetAPIKeys()
	if len(keys) > 0 {
		if !config.IsValidAPIKey(token) {
			users := usersDB.GetList()
			if findUser(users, token) == nil {
				return nil, 401, "Invalid API Key"
			}
		}
	}
	users := usersDB.GetList()
	user := findUser(users, token)
	if user != nil {
		quota, _ := user["quota"].(float64)
		used, _ := user["used_tokens"].(float64)
		if quota > 0 && used >= quota {
			return nil, 402, "Quota Exceeded"
		}
	}
	return &AuthContext{Token: token, User: user}, 0, ""
}

func findUser(users []interface{}, id string) map[string]interface{} {
	for _, u := range users {
		if m, ok := u.(map[string]interface{}); ok {
			if uid, _ := m["id"].(string); uid == id {
				return m
			}
		}
	}
	return nil
}

func DetectClientProfile(headers http.Header) string {
	ua := strings.ToLower(headers.Get("User-Agent"))
	if strings.Contains(ua, "claude") || strings.Contains(ua, "anthropic") {
		return "claude_code_openai"
	}
	if strings.Contains(ua, "codex") {
		return "codex_openai"
	}
	return "openclaw_openai"
}
