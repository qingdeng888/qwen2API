package pool

import (
	"sync"
	"time"
)

type Account struct {
	mu                  sync.RWMutex
	Email               string
	Password            string
	Token               string
	Cookies             string
	Username            string
	ActivationPending   bool
	StatusCode          string
	LastError           string
	Source              string
	EnvName             string
	Valid               bool
	LastUsed            float64
	Inflight            int
	RateLimitedUntil    float64
	LastRequestStarted  float64
	LastRequestFinished float64
	RateLimitStrikes    int
}

func NewAccountFromMap(data map[string]interface{}) *Account {
	a := &Account{
		Email:    getStr(data, "email"),
		Password: getStr(data, "password"),
		Token:    getStr(data, "token"),
		Cookies:  getStr(data, "cookies"),
		Username: getStr(data, "username"),
		Source:   getStr(data, "source"),
		EnvName:  getStr(data, "env_name"),
		StatusCode: getStr(data, "status_code"),
		LastError:  getStr(data, "last_error"),
	}
	if v, ok := data["activation_pending"].(bool); ok {
		a.ActivationPending = v
	}
	a.Valid = !a.ActivationPending
	if a.Source == "" {
		a.Source = "file"
	}
	if a.StatusCode == "" {
		if a.ActivationPending {
			a.StatusCode = "pending_activation"
		} else {
			a.StatusCode = "valid"
		}
	}
	return a
}

func NewAccountFromEnv(data map[string]string) *Account {
	return &Account{
		Email: data["email"], Password: data["password"],
		Token: data["token"], Source: data["source"],
		EnvName: data["env_name"], Valid: true, StatusCode: "valid",
	}
}

func (a *Account) IsRateLimited() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.RateLimitedUntil > float64(time.Now().Unix())
}

func (a *Account) GetStatusCode() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.ActivationPending {
		return "pending_activation"
	}
	if a.RateLimitedUntil > float64(time.Now().Unix()) {
		return "rate_limited"
	}
	if a.Valid {
		return "valid"
	}
	if a.StatusCode != "" {
		return a.StatusCode
	}
	return "invalid"
}

func (a *Account) ToMap() map[string]interface{} {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return map[string]interface{}{
		"email": a.Email, "password": a.Password,
		"token": a.Token, "cookies": a.Cookies,
		"username": a.Username, "activation_pending": a.ActivationPending,
		"status_code": a.StatusCode, "last_error": a.LastError,
		"source": a.Source, "env_name": a.EnvName,
	}
}

func getStr(data map[string]interface{}, key string) string {
	if v, ok := data[key].(string); ok {
		return v
	}
	return ""
}
