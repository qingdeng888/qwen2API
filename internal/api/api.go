package api

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/qingdeng888/qwen2API/internal/config"
	"github.com/qingdeng888/qwen2API/internal/database"
	"github.com/qingdeng888/qwen2API/internal/pool"
	"github.com/qingdeng888/qwen2API/internal/services"
	"github.com/qingdeng888/qwen2API/internal/services/modelmode"
	"github.com/qingdeng888/qwen2API/internal/upstream"
)

type AppContext struct {
	Config          *config.Settings
	AccountsDB      *database.JsonDB
	UsersDB         *database.JsonDB
	CapturesDB      *database.JsonDB
	ConfigDB        *database.JsonDB
	UploadedFilesDB *database.JsonDB
	AccountPool     *pool.AccountPool
	QwenClient      *upstream.QwenClient
	ChatIDPool      *services.ChatIDPool
}

func RegisterRoutes(mux *http.ServeMux, ctx *AppContext) {
	// CORS middleware wrapper with request logging
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS,HEAD,PATCH")
			w.Header().Set("Access-Control-Allow-Headers", "*")
			if r.Method == "OPTIONS" {
				w.WriteHeader(204)
				return
			}
			// Request logging when LOG_LEVEL=DEBUG
			if ctx.Config.LogLevel == "DEBUG" {
				log.Printf("[HTTP] %s %s from=%s", r.Method, r.URL.Path, r.RemoteAddr)
			}
			h(w, r)
		}
	}

	// Probes
	mux.HandleFunc("GET /healthz", wrap(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]interface{}{"status": "ok"}) }))
	mux.HandleFunc("GET /readyz", wrap(func(w http.ResponseWriter, _ *http.Request) {
		if ctx.AccountPool.Count() == 0 {
			writeJSON(w, 503, map[string]interface{}{"status": "no accounts"})
			return
		}
		writeJSON(w, 200, map[string]interface{}{"status": "ready", "accounts": ctx.AccountPool.Count()})
	}))
	mux.HandleFunc("GET /keepalive", wrap(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]interface{}{"ok": true, "service": "qwen2API"}) }))
	mux.HandleFunc("HEAD /keepalive", wrap(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	mux.HandleFunc("GET /api", wrap(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]interface{}{"status": "qwen2API Enterprise Gateway is running", "version": config.VERSION})
	}))

	// OpenAI Chat Completions
	chatHandler := wrap(func(w http.ResponseWriter, r *http.Request) { handleChat(w, r, ctx) })
	mux.HandleFunc("POST /v1/chat/completions", chatHandler)
	mux.HandleFunc("POST /chat/completions", chatHandler)
	mux.HandleFunc("POST /v1/responses", chatHandler)
	mux.HandleFunc("POST /responses", chatHandler)

	// Models
	mux.HandleFunc("GET /v1/models", wrap(func(w http.ResponseWriter, r *http.Request) { handleModels(w, r, ctx) }))
	mux.HandleFunc("GET /models", wrap(func(w http.ResponseWriter, r *http.Request) { handleModels(w, r, ctx) }))

	// Anthropic
	anthroHandler := wrap(func(w http.ResponseWriter, r *http.Request) { handleAnthropic(w, r, ctx) })
	mux.HandleFunc("POST /v1/messages", anthroHandler)
	mux.HandleFunc("POST /messages", anthroHandler)
	mux.HandleFunc("POST /anthropic/v1/messages", anthroHandler)

	// Count tokens
	countHandler := wrap(func(w http.ResponseWriter, r *http.Request) { handleCountTokens(w, r) })
	mux.HandleFunc("POST /v1/messages/count_tokens", countHandler)
	mux.HandleFunc("POST /anthropic/v1/messages/count_tokens", countHandler)
	mux.HandleFunc("POST /messages/count_tokens", countHandler)

	// Gemini
	mux.HandleFunc("POST /v1beta/models/", wrap(func(w http.ResponseWriter, r *http.Request) { handleGemini(w, r, ctx) }))
	mux.HandleFunc("POST /v1/models/", wrap(func(w http.ResponseWriter, r *http.Request) {
		// Check if it's a Gemini request or model detail
		if strings.Contains(r.URL.Path, ":generateContent") || strings.Contains(r.URL.Path, ":streamGenerateContent") {
			handleGemini(w, r, ctx)
		} else {
			handleModelDetail(w, r)
		}
	}))
	mux.HandleFunc("POST /models/", wrap(func(w http.ResponseWriter, r *http.Request) { handleGemini(w, r, ctx) }))

	// Embeddings
	embHandler := wrap(func(w http.ResponseWriter, r *http.Request) { handleEmbeddings(w, r) })
	mux.HandleFunc("POST /v1/embeddings", embHandler)
	mux.HandleFunc("POST /embeddings", embHandler)

	// Images
	imgHandler := wrap(func(w http.ResponseWriter, r *http.Request) { handleImages(w, r, ctx) })
	mux.HandleFunc("POST /v1/images/generations", imgHandler)
	mux.HandleFunc("POST /images/generations", imgHandler)

	// Videos
	mux.HandleFunc("POST /v1/videos/generations", wrap(func(w http.ResponseWriter, r *http.Request) { handleVideos(w, r, ctx) }))

	// Files
	mux.HandleFunc("POST /v1/files", wrap(func(w http.ResponseWriter, r *http.Request) { handleFileUpload(w, r, ctx) }))
	mux.HandleFunc("DELETE /v1/files/", wrap(func(w http.ResponseWriter, r *http.Request) { handleFileDelete(w, r, ctx) }))

	// Admin
	mux.HandleFunc("/api/admin/", wrap(func(w http.ResponseWriter, r *http.Request) { handleAdmin(w, r, ctx) }))
}

// =================== Chat Completions ===================

func handleChat(w http.ResponseWriter, r *http.Request, ctx *AppContext) {
	auth, code, msg := services.ResolveAuth(r, ctx.UsersDB, ctx.Config)
	if auth == nil {
		writeJSON(w, code, map[string]interface{}{"error": map[string]interface{}{"message": msg}})
		return
	}

	reqData := readJSON(r)
	if reqData == nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]interface{}{"message": "invalid JSON"}})
		return
	}

	modelName, _ := reqData["model"].(string)
	if modelName == "" {
		modelName = "gpt-4o"
	}
	mode := modelmode.Parse(modelName)
	stream, _ := reqData["stream"].(bool)

	prompt := buildPrompt(reqData)
	resolved := config.ResolveModel(mode.BaseModel)
	hasTools := hasToolsInRequest(reqData)

	log.Printf("[OAI] model=%s resolved=%s stream=%v tools=%v", modelName, resolved, stream, hasTools)

	opts := upstream.StreamOptions{
		HasCustomTools:  hasTools,
		ChatType:        mode.ChatType,
		EnableSearch:    mode.ChatType == "deep_research",
	}
	if mode.ForceThinking {
		t := true
		opts.ThinkingEnabled = &t
	}

	executor := upstream.NewExecutor(ctx.QwenClient, ctx.AccountPool, ctx.Config)
	reqCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	eventCh := executor.StreamWithRetry(reqCtx, resolved, prompt, opts)

	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher, _ := w.(http.Flusher)

		translator := services.NewOpenAITranslator(modelName)
		for item := range eventCh {
			if item.Error != nil {
				fmt.Fprintf(w, "data: {\"error\":{\"message\":\"%s\"}}\n\n", escJSON(item.Error.Error()))
				if flusher != nil { flusher.Flush() }
				return
			}
			if item.Event != nil {
				if chunk := translator.TranslateEvent(item.Event); chunk != "" {
					fmt.Fprint(w, chunk)
					if flusher != nil { flusher.Flush() }
				}
			}
		}
		fmt.Fprint(w, translator.FinishChunk())
		fmt.Fprint(w, translator.DoneChunk())
		if flusher != nil { flusher.Flush() }
	} else {
		var thinking, answer strings.Builder
		for item := range eventCh {
			if item.Error != nil {
				writeJSON(w, 500, map[string]interface{}{"error": map[string]interface{}{"message": item.Error.Error()}})
				return
			}
			if item.Event != nil && item.Event.Type == "delta" {
				if item.Event.Phase == "thinking_summary" || item.Event.Phase == "thinking" {
					thinking.WriteString(item.Event.Content)
				} else {
					answer.WriteString(item.Event.Content)
				}
			}
		}
		writeJSON(w, 200, services.BuildNonStreamResponse(modelName, answer.String(), thinking.String()))
	}
}

// =================== Anthropic ===================

func handleAnthropic(w http.ResponseWriter, r *http.Request, ctx *AppContext) {
	auth, code, msg := services.ResolveAuth(r, ctx.UsersDB, ctx.Config)
	if auth == nil {
		writeJSON(w, code, map[string]interface{}{"type": "error", "error": map[string]interface{}{"message": msg}})
		return
	}

	reqData := readJSON(r)
	if reqData == nil {
		writeJSON(w, 400, map[string]interface{}{"type": "error", "error": map[string]interface{}{"message": "invalid JSON"}})
		return
	}

	modelName, _ := reqData["model"].(string)
	if modelName == "" {
		modelName = "claude-3-5-sonnet"
	}
	mode := modelmode.Parse(modelName)
	stream, _ := reqData["stream"].(bool)
	hasTools := hasToolsInRequest(reqData)
	prompt := buildPrompt(reqData)
	resolved := config.ResolveModel(mode.BaseModel)

	log.Printf("[Anthropic] model=%s resolved=%s stream=%v", modelName, resolved, stream)

	opts := upstream.StreamOptions{HasCustomTools: hasTools, ChatType: mode.ChatType, EnableSearch: mode.ChatType == "deep_research"}
	if mode.ForceThinking {
		t := true
		opts.ThinkingEnabled = &t
	}

	executor := upstream.NewExecutor(ctx.QwenClient, ctx.AccountPool, ctx.Config)
	reqCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	eventCh := executor.StreamWithRetry(reqCtx, resolved, prompt, opts)

	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		translator := services.NewAnthropicTranslator(modelName)
		fmt.Fprint(w, translator.Start())
		if flusher != nil { flusher.Flush() }
		for item := range eventCh {
			if item.Error != nil {
				return
			}
			if item.Event != nil {
				if chunk := translator.TranslateEvent(item.Event); chunk != "" {
					fmt.Fprint(w, chunk)
					if flusher != nil { flusher.Flush() }
				}
			}
		}
		fmt.Fprint(w, translator.Finish())
		if flusher != nil { flusher.Flush() }
	} else {
		var thinking, answer strings.Builder
		for item := range eventCh {
			if item.Error != nil {
				writeJSON(w, 500, map[string]interface{}{"type": "error", "error": map[string]interface{}{"message": item.Error.Error()}})
				return
			}
			if item.Event != nil && item.Event.Type == "delta" {
				if item.Event.Phase == "thinking_summary" || item.Event.Phase == "thinking" {
					thinking.WriteString(item.Event.Content)
				} else {
					answer.WriteString(item.Event.Content)
				}
			}
		}
		content := []map[string]interface{}{}
		if thinking.Len() > 0 {
			content = append(content, map[string]interface{}{"type": "thinking", "thinking": thinking.String()})
		}
		content = append(content, map[string]interface{}{"type": "text", "text": answer.String()})
		writeJSON(w, 200, map[string]interface{}{
			"id": "msg_" + services.NewUUID()[:12], "type": "message", "role": "assistant",
			"model": modelName, "content": content, "stop_reason": "end_turn",
			"usage": map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
		})
	}
}

// =================== Gemini ===================

func handleGemini(w http.ResponseWriter, r *http.Request, ctx *AppContext) {
	auth, code, msg := services.ResolveAuth(r, ctx.UsersDB, ctx.Config)
	if auth == nil {
		writeJSON(w, code, map[string]interface{}{"error": map[string]interface{}{"message": msg}})
		return
	}

	path := r.URL.Path
	stream := strings.Contains(path, "streamGenerateContent")

	// Extract model from path
	model := extractGeminiModel(path)
	mode := modelmode.Parse(model)
	resolved := config.ResolveModel(mode.BaseModel)

	reqData := readJSON(r)
	if reqData == nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]interface{}{"message": "invalid JSON"}})
		return
	}

	prompt := extractGeminiPrompt(reqData)
	log.Printf("[Gemini] model=%s resolved=%s stream=%v", model, resolved, stream)

	opts := upstream.StreamOptions{ChatType: mode.ChatType, EnableSearch: mode.ChatType == "deep_research"}
	executor := upstream.NewExecutor(ctx.QwenClient, ctx.AccountPool, ctx.Config)
	reqCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	eventCh := executor.StreamWithRetry(reqCtx, resolved, prompt, opts)

	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for item := range eventCh {
			if item.Error != nil {
				return
			}
			if item.Event != nil && item.Event.Type == "delta" && item.Event.Content != "" {
				chunk := map[string]interface{}{"candidates": []map[string]interface{}{{"content": map[string]interface{}{"parts": []map[string]interface{}{{"text": item.Event.Content}}, "role": "model"}}}}
				d, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", d)
				if flusher != nil { flusher.Flush() }
			}
		}
	} else {
		var answer strings.Builder
		for item := range eventCh {
			if item.Error != nil {
				writeJSON(w, 500, map[string]interface{}{"error": map[string]interface{}{"message": item.Error.Error()}})
				return
			}
			if item.Event != nil && item.Event.Type == "delta" && (item.Event.Phase == "answer" || item.Event.Phase == "") {
				answer.WriteString(item.Event.Content)
			}
		}
		writeJSON(w, 200, map[string]interface{}{
			"candidates": []map[string]interface{}{{"content": map[string]interface{}{"parts": []map[string]interface{}{{"text": answer.String()}}, "role": "model"}, "finishReason": "STOP"}},
		})
	}
}

// =================== Models ===================

func handleModels(w http.ResponseWriter, _ *http.Request, _ *AppContext) {
	models := make([]map[string]interface{}, 0)
	seen := make(map[string]bool)
	for alias := range config.ModelMap {
		if seen[alias] { continue }
		seen[alias] = true
		models = append(models, map[string]interface{}{"id": alias, "object": "model", "created": 1700000000, "owned_by": "qwen"})
	}
	for _, m := range []string{"qwen3.6-plus", "qwen3.5-flash"} {
		if !seen[m] {
			models = append(models, map[string]interface{}{"id": m, "object": "model", "created": 1700000000, "owned_by": "qwen"})
		}
	}
	writeJSON(w, 200, map[string]interface{}{"object": "list", "data": models})
}

func handleModelDetail(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	modelID := parts[len(parts)-1]
	writeJSON(w, 200, map[string]interface{}{"id": modelID, "object": "model", "created": 1700000000, "owned_by": "qwen"})
}

// =================== Embeddings ===================

func handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	reqData := readJSON(r)
	if reqData == nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]interface{}{"message": "invalid JSON"}})
		return
	}
	inputs := extractInputs(reqData)
	model, _ := reqData["model"].(string)
	if model == "" { model = "text-embedding-ada-002" }
	data := make([]map[string]interface{}, len(inputs))
	for i, input := range inputs {
		data[i] = map[string]interface{}{"object": "embedding", "index": i, "embedding": deterministicEmbedding(input)}
	}
	writeJSON(w, 200, map[string]interface{}{"object": "list", "data": data, "model": model, "usage": map[string]interface{}{"prompt_tokens": len(inputs) * 10, "total_tokens": len(inputs) * 10}})
}

// =================== Images ===================

func handleImages(w http.ResponseWriter, r *http.Request, ctx *AppContext) {
	auth, code, msg := services.ResolveAuth(r, ctx.UsersDB, ctx.Config)
	if auth == nil { writeJSON(w, code, map[string]interface{}{"error": map[string]interface{}{"message": msg}}); return }
	reqData := readJSON(r)
	if reqData == nil { writeJSON(w, 400, map[string]interface{}{"error": map[string]interface{}{"message": "invalid JSON"}}); return }
	prompt, _ := reqData["prompt"].(string)
	if prompt == "" { writeJSON(w, 400, map[string]interface{}{"error": map[string]interface{}{"message": "prompt required"}}); return }
	size, _ := reqData["size"].(string)
	ratio := upstream.NormalizeImageRatioFromSize(size)
	opts := upstream.StreamOptions{ChatType: "image_gen", ImageOptions: map[string]interface{}{"ratio": ratio}}
	executor := upstream.NewExecutor(ctx.QwenClient, ctx.AccountPool, ctx.Config)
	reqCtx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	eventCh := executor.StreamWithRetry(reqCtx, "qwen3.6-plus", prompt, opts)
	var content strings.Builder
	for item := range eventCh {
		if item.Error != nil { writeJSON(w, 500, map[string]interface{}{"error": map[string]interface{}{"message": item.Error.Error()}}); return }
		if item.Event != nil && item.Event.Content != "" { content.WriteString(item.Event.Content) }
	}
	urls := extractImageURLs(content.String())
	data := make([]map[string]interface{}, 0)
	for _, u := range urls { data = append(data, map[string]interface{}{"url": u}) }
	if len(data) == 0 { data = append(data, map[string]interface{}{"url": "", "revised_prompt": content.String()}) }
	writeJSON(w, 200, map[string]interface{}{"created": time.Now().Unix(), "data": data})
}

// =================== Videos ===================

func handleVideos(w http.ResponseWriter, r *http.Request, ctx *AppContext) {
	auth, code, msg := services.ResolveAuth(r, ctx.UsersDB, ctx.Config)
	if auth == nil { writeJSON(w, code, map[string]interface{}{"error": map[string]interface{}{"message": msg}}); return }
	reqData := readJSON(r)
	if reqData == nil { writeJSON(w, 400, map[string]interface{}{"error": map[string]interface{}{"message": "invalid JSON"}}); return }
	prompt, _ := reqData["prompt"].(string)
	if prompt == "" { writeJSON(w, 400, map[string]interface{}{"error": map[string]interface{}{"message": "prompt required"}}); return }
	opts := upstream.StreamOptions{ChatType: "t2v"}
	executor := upstream.NewExecutor(ctx.QwenClient, ctx.AccountPool, ctx.Config)
	reqCtx, cancel := context.WithTimeout(r.Context(), 300*time.Second)
	defer cancel()
	eventCh := executor.StreamWithRetry(reqCtx, "qwen3.6-plus", prompt, opts)
	var content strings.Builder
	for item := range eventCh {
		if item.Error != nil { writeJSON(w, 500, map[string]interface{}{"error": map[string]interface{}{"message": item.Error.Error()}}); return }
		if item.Event != nil && item.Event.Content != "" { content.WriteString(item.Event.Content) }
	}
	writeJSON(w, 200, map[string]interface{}{"created": time.Now().Unix(), "content": content.String()})
}

// =================== Files ===================

func handleFileUpload(w http.ResponseWriter, r *http.Request, ctx *AppContext) {
	auth, code, msg := services.ResolveAuth(r, ctx.UsersDB, ctx.Config)
	if auth == nil { writeJSON(w, code, map[string]interface{}{"error": map[string]interface{}{"message": msg}}); return }
	r.ParseMultipartForm(32 << 20)
	file, header, err := r.FormFile("file")
	if err != nil { writeJSON(w, 400, map[string]interface{}{"error": map[string]interface{}{"message": "file required"}}); return }
	defer file.Close()
	purpose := r.FormValue("purpose")
	if purpose == "" { purpose = "assistants" }
	fileID := "file-" + services.NewUUID()[:12]
	entry := map[string]interface{}{"id": fileID, "object": "file", "bytes": header.Size, "created_at": time.Now().Unix(), "filename": header.Filename, "purpose": purpose, "status": "processed"}
	files := ctx.UploadedFilesDB.GetList()
	files = append(files, entry)
	ctx.UploadedFilesDB.Save(files)
	writeJSON(w, 200, entry)
}

func handleFileDelete(w http.ResponseWriter, r *http.Request, ctx *AppContext) {
	auth, code, msg := services.ResolveAuth(r, ctx.UsersDB, ctx.Config)
	if auth == nil { writeJSON(w, code, map[string]interface{}{"error": map[string]interface{}{"message": msg}}); return }
	parts := strings.Split(r.URL.Path, "/")
	fileID := parts[len(parts)-1]
	files := ctx.UploadedFilesDB.GetList()
	newFiles := make([]interface{}, 0)
	found := false
	for _, f := range files {
		if m, ok := f.(map[string]interface{}); ok && m["id"] == fileID { found = true; continue }
		newFiles = append(newFiles, f)
	}
	if !found { writeJSON(w, 404, map[string]interface{}{"error": map[string]interface{}{"message": "not found"}}); return }
	ctx.UploadedFilesDB.Save(newFiles)
	writeJSON(w, 200, map[string]interface{}{"id": fileID, "object": "file", "deleted": true})
}

// =================== Count Tokens ===================

func handleCountTokens(w http.ResponseWriter, r *http.Request) {
	reqData := readJSON(r)
	total := 0
	if msgs, ok := reqData["messages"].([]interface{}); ok {
		for _, m := range msgs {
			if msg, ok := m.(map[string]interface{}); ok {
				if c, ok := msg["content"].(string); ok { total += len(c) }
			}
		}
	}
	writeJSON(w, 200, map[string]interface{}{"input_tokens": total / 4})
}

// =================== Admin ===================

func handleAdmin(w http.ResponseWriter, r *http.Request, ctx *AppContext) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin")
	path = strings.TrimPrefix(path, "/")

	// Login endpoint does not require auth
	if path == "login" && r.Method == "POST" {
		handleAdminLogin(w, r, ctx)
		return
	}

	// All other admin endpoints require valid token
	token := extractAdminToken(r)
	if token != ctx.Config.PanelPassword {
		log.Printf("[Admin] unauthorized: path=%s token=%s", path, trunc(token, 8))
		writeJSON(w, 401, map[string]interface{}{"error": "unauthorized"})
		return
	}

	if ctx.Config.LogLevel == "DEBUG" {
		log.Printf("[Admin] request: %s %s", r.Method, path)
	}

	switch {
	case path == "accounts" && r.Method == "GET":
		accounts := ctx.AccountPool.Accounts()
		result := make([]map[string]interface{}, 0)
		for _, acc := range accounts {
			result = append(result, map[string]interface{}{"email": acc.Email, "status": acc.GetStatusCode(), "status_code": acc.GetStatusCode(), "source": acc.Source, "inflight": acc.Inflight, "valid": acc.Valid})
		}
		log.Printf("[Admin] 获取账号列表 total=%d", len(result))
		writeJSON(w, 200, map[string]interface{}{"accounts": result, "total": len(result)})
	case path == "accounts" && r.Method == "POST":
		data := readJSON(r)
		if data == nil { writeJSON(w, 400, map[string]interface{}{"error": "invalid JSON", "ok": false}); return }
		tk, _ := data["token"].(string)
		email, _ := data["email"].(string)
		if tk == "" { writeJSON(w, 400, map[string]interface{}{"error": "token required", "ok": false}); return }
		log.Printf("[Admin] 添加账号 email=%s token=%s...", email, trunc(tk, 10))
		ctx.AccountPool.AddAccount(&pool.Account{Email: email, Token: tk, Valid: true, StatusCode: "valid", Source: "managed"})
		ctx.AccountPool.SaveToDB()
		writeJSON(w, 200, map[string]interface{}{"ok": true, "status": "ok", "email": email})
	case path == "api-keys" && r.Method == "GET":
		writeJSON(w, 200, map[string]interface{}{"keys": config.ListAPIKeyItems()})
	case path == "api-keys" && r.Method == "POST":
		data := readJSON(r)
		key, _ := data["key"].(string)
		if config.AddAPIKey(key) { writeJSON(w, 200, map[string]interface{}{"status": "ok"}) } else { writeJSON(w, 409, map[string]interface{}{"error": "exists"}) }
	case path == "users" && r.Method == "GET":
		writeJSON(w, 200, map[string]interface{}{"users": ctx.UsersDB.GetList()})
	case path == "status" && r.Method == "GET":
		accounts := ctx.AccountPool.Accounts()
		valid, rateLimited, invalid, inUse := 0, 0, 0, 0
		perAccount := make([]map[string]interface{}, 0, len(accounts))
		for _, a := range accounts {
			st := a.GetStatusCode()
			switch st {
			case "valid":
				valid++
			case "rate_limited":
				rateLimited++
			default:
				invalid++
			}
			a_inflight := a.Inflight
			inUse += a_inflight
			perAccount = append(perAccount, map[string]interface{}{
				"email":                a.Email,
				"status":              st,
				"inflight":            a_inflight,
				"max_inflight":        ctx.Config.MaxInflightPerAccount,
				"consecutive_failures": 0,
				"rate_limit_strikes":  a.RateLimitStrikes,
				"last_request_finished": a.LastRequestFinished,
			})
		}
		chatPoolInfo := map[string]interface{}{
			"total_cached":       0,
			"target_per_account": ctx.Config.ChatIDPrewarmTargetPerAccount,
			"ttl_seconds":        ctx.Config.ChatIDPrewarmTTLSeconds,
			"per_account":        map[string]int{},
		}
		writeJSON(w, 200, map[string]interface{}{
			"accounts": map[string]interface{}{
				"total":                    len(accounts),
				"valid":                    valid,
				"rate_limited":             rateLimited,
				"invalid":                  invalid,
				"in_use":                   inUse,
				"global_in_use":            inUse,
				"waiting":                  0,
				"max_inflight_per_account": ctx.Config.MaxInflightPerAccount,
				"max_queue_size":           ctx.Config.AccountReadySetThreshold,
			},
			"per_account":  perAccount,
			"chat_id_pool": chatPoolInfo,
			"runtime":      map[string]interface{}{"asyncio_running_tasks": 0},
		})
	case path == "config" && r.Method == "GET":
		writeJSON(w, 200, ctx.ConfigDB.GetMap())
	case path == "config" && r.Method == "PUT":
		data := readJSON(r)
		current := ctx.ConfigDB.GetMap()
		for k, v := range data { current[k] = v }
		ctx.ConfigDB.Save(current)
		writeJSON(w, 200, current)
	case strings.HasPrefix(path, "accounts/") && r.Method == "DELETE":
		// DELETE /api/admin/accounts/:email
		email := strings.TrimPrefix(path, "accounts/")
		log.Printf("[Admin] 删除账号 email=%s", email)
		removed := ctx.AccountPool.RemoveByEmail(email)
		if removed {
			ctx.AccountPool.SaveToDB()
			writeJSON(w, 200, map[string]interface{}{"ok": true})
		} else {
			writeJSON(w, 404, map[string]interface{}{"error": "account not found", "ok": false})
		}
	case strings.HasPrefix(path, "accounts/") && strings.HasSuffix(path, "/verify") && r.Method == "POST":
		// POST /api/admin/accounts/:email/verify
		email := strings.TrimPrefix(path, "accounts/")
		email = strings.TrimSuffix(email, "/verify")
		log.Printf("[Admin] 验证账号 email=%s", email)
		acc := ctx.AccountPool.FindByEmail(email)
		if acc == nil {
			writeJSON(w, 404, map[string]interface{}{"valid": false, "error": "account not found"})
			return
		}
		// Verify by calling upstream to check token validity
		status, _, err := ctx.QwenClient.RequestJSON("GET", "/api/models", acc.Token, nil, 15*time.Second)
		if err != nil {
			writeJSON(w, 200, map[string]interface{}{"valid": false, "error": err.Error(), "status_code": "auth_error"})
			return
		}
		if status == 200 {
			writeJSON(w, 200, map[string]interface{}{"valid": true, "status_code": "valid"})
		} else if status == 401 || status == 403 {
			ctx.AccountPool.MarkInvalid(acc)
			writeJSON(w, 200, map[string]interface{}{"valid": false, "status_code": "auth_error", "error": fmt.Sprintf("HTTP %d", status)})
		} else {
			writeJSON(w, 200, map[string]interface{}{"valid": false, "status_code": "unknown", "error": fmt.Sprintf("HTTP %d", status)})
		}
	case path == "verify" && r.Method == "POST":
		// POST /api/admin/verify — verify all accounts
		log.Printf("[Admin] 全量验证账号")
		writeJSON(w, 200, map[string]interface{}{"ok": true, "concurrency": 1})
	default:
		writeJSON(w, 404, map[string]interface{}{"error": "not found"})
	}
}

// =================== Helpers ===================

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func readJSON(r *http.Request) map[string]interface{} {
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 { return nil }
	var data map[string]interface{}
	if json.Unmarshal(body, &data) != nil { return nil }
	return data
}

func buildPrompt(reqData map[string]interface{}) string {
	var sb strings.Builder
	if sys, ok := reqData["system"].(string); ok && sys != "" {
		sb.WriteString("[System]\n" + sys + "\n\n")
	}
	if msgs, ok := reqData["messages"].([]interface{}); ok {
		for _, m := range msgs {
			msg, ok := m.(map[string]interface{})
			if !ok { continue }
			role, _ := msg["role"].(string)
			content := extractContent(msg)
			if content == "" { continue }
			switch role {
			case "system":
				sb.WriteString("[System]\n" + content + "\n\n")
			case "user":
				sb.WriteString("[User]\n" + content + "\n\n")
			case "assistant":
				sb.WriteString("[Assistant]\n" + content + "\n\n")
			case "tool":
				sb.WriteString("[Tool Result]\n" + content + "\n\n")
			}
		}
	}
	if tools := extractToolDefs(reqData); tools != "" {
		sb.WriteString(tools)
	}
	return sb.String()
}

func extractContent(msg map[string]interface{}) string {
	if s, ok := msg["content"].(string); ok { return s }
	if parts, ok := msg["content"].([]interface{}); ok {
		var texts []string
		for _, p := range parts {
			if pm, ok := p.(map[string]interface{}); ok {
				if pm["type"] == "text" { if t, ok := pm["text"].(string); ok { texts = append(texts, t) } }
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

func hasToolsInRequest(reqData map[string]interface{}) bool {
	tools, ok := reqData["tools"].([]interface{})
	return ok && len(tools) > 0
}

func extractToolDefs(reqData map[string]interface{}) string {
	tools, ok := reqData["tools"].([]interface{})
	if !ok || len(tools) == 0 { return "" }
	var sb strings.Builder
	sb.WriteString("\n## Available Tools\n\n")
	for _, t := range tools {
		tool, ok := t.(map[string]interface{})
		if !ok { continue }
		var name, desc string
		if fn, ok := tool["function"].(map[string]interface{}); ok {
			name, _ = fn["name"].(string)
			desc, _ = fn["description"].(string)
		} else {
			name, _ = tool["name"].(string)
			desc, _ = tool["description"].(string)
		}
		sb.WriteString(fmt.Sprintf("### %s\n%s\n\n", name, desc))
	}
	return sb.String()
}

func extractGeminiModel(path string) string {
	// /v1/models/gemini-2.5-pro:generateContent -> gemini-2.5-pro
	re := regexp.MustCompile(`/models/([^/:]+)`)
	m := re.FindStringSubmatch(path)
	if len(m) >= 2 { return m[1] }
	return "gemini-2.5-pro"
}

func extractGeminiPrompt(reqData map[string]interface{}) string {
	contents, _ := reqData["contents"].([]interface{})
	var lines []string
	for _, c := range contents {
		msg, _ := c.(map[string]interface{})
		if msg == nil || msg["role"] != "user" { continue }
		parts, _ := msg["parts"].([]interface{})
		for _, p := range parts {
			pm, _ := p.(map[string]interface{})
			if pm == nil { continue }
			if t, ok := pm["text"].(string); ok && t != "" { lines = append(lines, t) }
		}
	}
	return strings.Join(lines, "\n")
}

func extractInputs(reqData map[string]interface{}) []string {
	switch v := reqData["input"].(type) {
	case string: return []string{v}
	case []interface{}:
		r := make([]string, 0, len(v))
		for _, item := range v { if s, ok := item.(string); ok { r = append(r, s) } }
		return r
	}
	return []string{""}
}

func deterministicEmbedding(text string) []float64 {
	h := sha256.Sum256([]byte(text))
	emb := make([]float64, 1536)
	for i := 0; i < 1536; i++ {
		idx := i % 28
		bits := binary.LittleEndian.Uint32(h[idx : idx+4])
		emb[i] = float64(bits)/float64(math.MaxUint32)*2.0 - 1.0
		if i%28 == 27 { h = sha256.Sum256(h[:]) }
	}
	var norm float64
	for _, v := range emb { norm += v * v }
	norm = math.Sqrt(norm)
	if norm > 0 { for i := range emb { emb[i] /= norm } }
	return emb
}

func extractImageURLs(text string) []string {
	re := regexp.MustCompile(`https?://[^\s"'\]<>]+\.(?:png|jpg|jpeg|gif|webp)[^\s"'\]<>]*`)
	return re.FindAllString(text, -1)
}

func extractAdminToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") { return auth[7:] }
	if k := r.Header.Get("x-api-key"); k != "" { return k }
	return r.URL.Query().Get("key")
}

func handleAdminLogin(w http.ResponseWriter, r *http.Request, ctx *AppContext) {
	data := readJSON(r)
	if data == nil {
		log.Printf("[Admin] login: invalid JSON body")
		writeJSON(w, 400, map[string]interface{}{"error": "invalid JSON"})
		return
	}
	password, _ := data["password"].(string)
	if password == "" {
		log.Printf("[Admin] login: empty password")
		writeJSON(w, 400, map[string]interface{}{"error": "密码不能为空"})
		return
	}
	// Check against PANEL_PASSWORD
	panelPwd := ctx.Config.PanelPassword
	if password != panelPwd {
		log.Printf("[Admin] login: wrong password attempt from %s", r.RemoteAddr)
		writeJSON(w, 401, map[string]interface{}{"error": "密码错误"})
		return
	}
	log.Printf("[Admin] login: success from %s", r.RemoteAddr)
	// Return the panel password as token for subsequent admin requests
	writeJSON(w, 200, map[string]interface{}{"token": ctx.Config.PanelPassword, "message": "ok"})
}

func escJSON(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
