package config

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const VERSION = "2.0.0"

type Settings struct {
	Port          int
	Workers       int
	PanelPassword string

	MaxInflightPerAccount       int
	BrowserStreamTimeoutSeconds int

	MaxRetries             int
	AccountMinIntervalMS   int
	RequestJitterMinMS     int
	RequestJitterMaxMS     int
	RateLimitBaseCooldown  int
	RateLimitMaxCooldown   int
	AccountReadySetThreshold int

	ChatDeleteRetryAttempts       int
	ChatDeleteRetryDelaySeconds   float64
	ChatIDPrewarmTargetPerAccount int
	ChatIDPrewarmTTLSeconds       int
	ChatIDPrewarmMaxConcurrency   int
	TraceResponseFingerprints     bool
	TraceResponseTailChars        int

	LogLevel string

	AccountsFile        string
	UsersFile           string
	CapturesFile        string
	ConfigFile          string
	ContextGeneratedDir string
	ContextCacheFile    string
	UploadedFilesFile   string
	ContextAffinityFile string

	ContextInlineMaxChars            int
	ContextForceFileMaxChars         int
	ContextAttachmentTTLSeconds      int
	ContextUploadParseTimeoutSeconds int
}

func Load() *Settings {
	return &Settings{
		Port:          envInt("PORT", 7860),
		Workers:       envInt("WORKERS", 1),
		PanelPassword: envStr("PANEL_PASSWORD", "admin"),

		MaxInflightPerAccount:       envInt("MAX_INFLIGHT_PER_ACCOUNT", 2),
		BrowserStreamTimeoutSeconds: envInt("BROWSER_STREAM_TIMEOUT_SECONDS", 1800),

		MaxRetries:             envInt("MAX_RETRIES", 3),
		AccountMinIntervalMS:   envInt("ACCOUNT_MIN_INTERVAL_MS", 0),
		RequestJitterMinMS:     envInt("REQUEST_JITTER_MIN_MS", 0),
		RequestJitterMaxMS:     envInt("REQUEST_JITTER_MAX_MS", 0),
		RateLimitBaseCooldown:  envInt("RATE_LIMIT_BASE_COOLDOWN", 600),
		RateLimitMaxCooldown:   envInt("RATE_LIMIT_MAX_COOLDOWN", 3600),
		AccountReadySetThreshold: envInt("ACCOUNT_READY_SET_THRESHOLD", 128),

		ChatDeleteRetryAttempts:       envInt("CHAT_DELETE_RETRY_ATTEMPTS", 3),
		ChatDeleteRetryDelaySeconds:   envFloat("CHAT_DELETE_RETRY_DELAY_SECONDS", 0.5),
		ChatIDPrewarmTargetPerAccount: envInt("CHAT_ID_PREWARM_TARGET_PER_ACCOUNT", 5),
		ChatIDPrewarmTTLSeconds:       envInt("CHAT_ID_PREWARM_TTL_SECONDS", 120),
		ChatIDPrewarmMaxConcurrency:   envInt("CHAT_ID_PREWARM_MAX_CONCURRENCY", 16),
		TraceResponseFingerprints:     envBool("TRACE_RESPONSE_FINGERPRINTS", false),
		TraceResponseTailChars:        envInt("TRACE_RESPONSE_TAIL_CHARS", 160),

		LogLevel: envStr("LOG_LEVEL", "INFO"),

		AccountsFile:        envStr("ACCOUNTS_FILE", "data/accounts.json"),
		UsersFile:           envStr("USERS_FILE", "data/users.json"),
		CapturesFile:        envStr("CAPTURES_FILE", "data/captures.json"),
		ConfigFile:          envStr("CONFIG_FILE", "data/config.json"),
		ContextGeneratedDir: envStr("CONTEXT_GENERATED_DIR", "data/context_files"),
		ContextCacheFile:    envStr("CONTEXT_CACHE_FILE", "data/context_cache.json"),
		UploadedFilesFile:   envStr("UPLOADED_FILES_FILE", "data/uploaded_files.json"),
		ContextAffinityFile: envStr("CONTEXT_AFFINITY_FILE", "data/session_affinity.json"),

		ContextInlineMaxChars:            envInt("CONTEXT_INLINE_MAX_CHARS", 4000),
		ContextForceFileMaxChars:         envInt("CONTEXT_FORCE_FILE_MAX_CHARS", 10000),
		ContextAttachmentTTLSeconds:      envInt("CONTEXT_ATTACHMENT_TTL_SECONDS", 1800),
		ContextUploadParseTimeoutSeconds: envInt("CONTEXT_UPLOAD_PARSE_TIMEOUT_SECONDS", 60),
	}
}

// Model mapping - only keeps essential aliases, passes through qwen model names directly
var ModelMap = map[string]string{
	"qwen":       "qwen-max-latest",
	"qwen-max":   "qwen-max-latest",
	"qwen-plus":  "qwen-plus-latest",
	"qwen-turbo": "qwen-turbo-latest",
}

func ResolveModel(name string) string {
	if m, ok := ModelMap[name]; ok {
		return m
	}
	return name
}

// GetDefaultModel returns the default upstream model for prewarming
func GetDefaultModel() string {
	return "qwen-max-latest"
}

// DynamicModels stores models fetched from upstream (cached 5 min)
var (
	dynamicModelsMu   sync.RWMutex
	dynamicModels     []map[string]interface{}
	dynamicModelsTime int64
)

func GetDynamicModels() []map[string]interface{} {
	dynamicModelsMu.RLock()
	defer dynamicModelsMu.RUnlock()
	return dynamicModels
}

func SetDynamicModels(models []map[string]interface{}) {
	dynamicModelsMu.Lock()
	defer dynamicModelsMu.Unlock()
	dynamicModels = models
	dynamicModelsTime = time.Now().Unix()
}

func DynamicModelsCacheExpired() bool {
	dynamicModelsMu.RLock()
	defer dynamicModelsMu.RUnlock()
	return dynamicModelsTime == 0 || time.Now().Unix()-dynamicModelsTime > 300
}

// API Key management
var (
	apiKeysMu      sync.RWMutex
	envAPIKeys     []string
	managedAPIKeys []string
	allAPIKeys     map[string]struct{}
	apiKeysFile    = "data/api_keys.json"
)

func init() {
	envAPIKeys = loadEnvAPIKeys()
	managedAPIKeys = loadManagedAPIKeys()
	allAPIKeys = make(map[string]struct{})
	syncAPIKeys()
}

func GetAPIKeys() map[string]struct{} {
	apiKeysMu.RLock()
	defer apiKeysMu.RUnlock()
	cp := make(map[string]struct{}, len(allAPIKeys))
	for k, v := range allAPIKeys {
		cp[k] = v
	}
	return cp
}

func IsValidAPIKey(key string) bool {
	apiKeysMu.RLock()
	defer apiKeysMu.RUnlock()
	if len(allAPIKeys) == 0 {
		return true
	}
	_, ok := allAPIKeys[key]
	return ok
}

func AddAPIKey(key string) bool {
	apiKeysMu.Lock()
	defer apiKeysMu.Unlock()
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	if _, exists := allAPIKeys[key]; exists {
		return false
	}
	managedAPIKeys = append(managedAPIKeys, key)
	saveManagedAPIKeys()
	syncAPIKeys()
	return true
}

func RemoveAPIKey(key string) string {
	apiKeysMu.Lock()
	defer apiKeysMu.Unlock()
	key = strings.TrimSpace(key)
	for _, ek := range envAPIKeys {
		if ek == key {
			return "env"
		}
	}
	found := false
	filtered := make([]string, 0, len(managedAPIKeys))
	for _, mk := range managedAPIKeys {
		if mk == key {
			found = true
			continue
		}
		filtered = append(filtered, mk)
	}
	if !found {
		return "missing"
	}
	managedAPIKeys = filtered
	saveManagedAPIKeys()
	syncAPIKeys()
	return "removed"
}

func ListAPIKeyItems() []map[string]string {
	apiKeysMu.RLock()
	defer apiKeysMu.RUnlock()
	items := make([]map[string]string, 0)
	envSet := make(map[string]struct{})
	for _, k := range envAPIKeys {
		envSet[k] = struct{}{}
		items = append(items, map[string]string{"key": k, "source": "env", "label": "环境变量注入 Key"})
	}
	for _, k := range managedAPIKeys {
		if _, inEnv := envSet[k]; inEnv {
			continue
		}
		items = append(items, map[string]string{"key": k, "source": "managed", "label": "面板创建 Key"})
	}
	return items
}

func syncAPIKeys() {
	allAPIKeys = make(map[string]struct{})
	for _, k := range envAPIKeys {
		allAPIKeys[k] = struct{}{}
	}
	for _, k := range managedAPIKeys {
		allAPIKeys[k] = struct{}{}
	}
}

func loadEnvAPIKeys() []string {
	var values []string
	for _, name := range []string{"QWEN_API_KEY", "QWEN_API_KEYS", "API_KEYS"} {
		if raw := os.Getenv(name); raw != "" {
			values = append(values, splitKeyValues(raw)...)
		}
	}
	re := regexp.MustCompile(`^QWEN_API_KEY_(\d+)$`)
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 && re.MatchString(parts[0]) {
			values = append(values, splitKeyValues(parts[1])...)
		}
	}
	return dedupeNonempty(values)
}

func loadManagedAPIKeys() []string {
	data, err := os.ReadFile(apiKeysFile)
	if err != nil {
		return nil
	}
	var obj struct {
		Keys json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	var strKeys string
	if json.Unmarshal(obj.Keys, &strKeys) == nil {
		return dedupeNonempty(splitKeyValues(strKeys))
	}
	var listKeys []string
	if json.Unmarshal(obj.Keys, &listKeys) == nil {
		return dedupeNonempty(listKeys)
	}
	return nil
}

func saveManagedAPIKeys() {
	os.MkdirAll("data", 0755)
	data, _ := json.MarshalIndent(map[string]interface{}{"keys": managedAPIKeys}, "", "  ")
	os.WriteFile(apiKeysFile, data, 0644)
}

func LoadEnvAccounts() []map[string]string {
	re := regexp.MustCompile(`^QWEN_ACCOUNT_(\d+)$`)
	type ne struct {
		idx  int
		name string
		val  string
	}
	var envs []ne
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			m := re.FindStringSubmatch(parts[0])
			if m != nil {
				idx, _ := strconv.Atoi(m[1])
				envs = append(envs, ne{idx, parts[0], parts[1]})
			}
		}
	}
	sort.Slice(envs, func(i, j int) bool { return envs[i].idx < envs[j].idx })

	accounts := make([]map[string]string, 0)
	for _, e := range envs {
		parts := strings.SplitN(e.val, ";", 3)
		token := strings.TrimSpace(parts[0])
		if token == "" {
			continue
		}
		email, password := "", ""
		if len(parts) >= 2 {
			email = strings.TrimSpace(parts[1])
		}
		if email == "" {
			email = "env_" + strconv.Itoa(e.idx) + "@qwen"
		}
		if len(parts) >= 3 {
			password = strings.TrimSpace(parts[2])
		}
		accounts = append(accounts, map[string]string{
			"email": email, "password": password, "token": token,
			"source": "env", "env_name": e.name,
		})
	}
	return accounts
}

func splitKeyValues(value string) []string {
	re := regexp.MustCompile(`[\s,;]+`)
	parts := re.Split(value, -1)
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			result = append(result, p)
		}
	}
	return result
}

func dedupeNonempty(values []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		if _, exists := seen[v]; exists {
			continue
		}
		seen[v] = struct{}{}
		result = append(result, v)
	}
	return result
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
