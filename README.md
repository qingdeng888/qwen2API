# qwen2API Enterprise Gateway (Go)

[![Stars](https://img.shields.io/github/stars/qingdeng888/qwen2API?style=flat-square)](https://github.com/qingdeng888/qwen2API/stargazers)

**High-performance Go rewrite** of the qwen2API gateway. Converts the Qwen (通义千问) web interface into OpenAI, Anthropic Claude, and Google Gemini compatible API endpoints.

## Why Go?

- **~10x less memory** compared to the Python version
- **Native concurrency** with goroutines (no GIL)
- **Single binary deployment** — no Python/pip dependencies
- **Sub-millisecond latency overhead**
- **Zero external Go dependencies** — uses only the standard library

## Features

- ✅ OpenAI Chat Completions (`/v1/chat/completions`) — streaming & non-streaming
- ✅ OpenAI Responses API (`/v1/responses`)
- ✅ Anthropic Messages API (`/v1/messages`, `/anthropic/v1/messages`)
- ✅ Gemini GenerateContent & StreamGenerateContent
- ✅ Model listing (`/v1/models`)
- ✅ Image generation (`/v1/images/generations`)
- ✅ Video generation (`/v1/videos/generations`)
- ✅ Embeddings (compatibility placeholder)
- ✅ File upload/delete (`/v1/files`)
- ✅ Admin dashboard API (`/api/admin/*`)
- ✅ Multi-account pool with concurrency control
- ✅ Rate limit backoff with exponential cooldown
- ✅ Chat ID prewarming (reduces latency by 500ms–6s)
- ✅ Keepalive service for PaaS platforms
- ✅ Health probes (`/healthz`, `/readyz`)
- ✅ Thinking mode support (reasoning content)
- ✅ Model mode suffixes (`-thinking`, `-deep-research`, `-image`, `-video`)
- ✅ Tool calling bridge (QNML/JSON/XML parsing)

## Quick Start

### Docker (Recommended)

```bash
docker build -t qwen2api .
docker run -d -p 7860:7860 \
  -e PANEL_PASSWORD=your-panel-password \
  -e QWEN_ACCOUNT_1="your-token;email@example.com" \
  -v ./data:/workspace/data \
  qwen2api
```

### Binary

```bash
# Build
go build -o qwen2api ./cmd/server

# Run
export PANEL_PASSWORD=your-panel-password
export QWEN_ACCOUNT_1="your-token;email@example.com"
./qwen2api
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | 7860 | HTTP listen port |
| `PANEL_PASSWORD` | admin | Web管理面板登录密码 |
| `QWEN_API_KEY` | — | Client API key (comma-separated for multiple) |
| `QWEN_ACCOUNT_N` | — | Qwen accounts (format: `token;email;password`) |
| `MAX_INFLIGHT_PER_ACCOUNT` | 2 | Max concurrent requests per account |
| `MAX_RETRIES` | 3 | Max retry attempts per request |
| `RATE_LIMIT_BASE_COOLDOWN` | 600 | Rate limit cooldown in seconds |
| `RATE_LIMIT_MAX_COOLDOWN` | 3600 | Max cooldown with exponential backoff |
| `CHAT_ID_PREWARM_TARGET_PER_ACCOUNT` | 5 | Pre-warmed chat IDs per account |
| `CHAT_ID_PREWARM_TTL_SECONDS` | 120 | TTL for prewarmed chat IDs |
| `KEEPALIVE_URL` | — | URL for periodic keepalive requests |
| `KEEPALIVE_INTERVAL` | 60 | Keepalive interval in seconds |
| `LOG_LEVEL` | INFO | Log level |

## API Endpoints

| Protocol | Path | Description |
|----------|------|-------------|
| OpenAI Chat | `POST /v1/chat/completions` | Chat completions (stream/non-stream) |
| OpenAI Responses | `POST /v1/responses` | Responses API |
| OpenAI Models | `GET /v1/models` | List available models |
| OpenAI Images | `POST /v1/images/generations` | Image generation |
| OpenAI Videos | `POST /v1/videos/generations` | Video generation |
| OpenAI Embeddings | `POST /v1/embeddings` | Embeddings (placeholder) |
| OpenAI Files | `POST /v1/files` | File upload |
| Anthropic | `POST /v1/messages` | Claude Messages API |
| Gemini | `POST /v1/models/{model}:generateContent` | Gemini API |
| Admin | `GET /api/admin/status` | System status |
| Health | `GET /healthz` | Liveness probe |
| Ready | `GET /readyz` | Readiness probe |

## Model Mapping

Popular model names are automatically mapped to Qwen models:

| Input Model | Mapped To |
|-------------|-----------|
| gpt-4o, gpt-4, o1, o3, claude-3.5-sonnet, gemini-2.5-pro | qwen3.6-plus |
| gpt-4o-mini, gpt-3.5-turbo, o1-mini, claude-3-haiku, gemini-2.5-flash | qwen3.5-flash |

## Architecture

```
cmd/server/         - Entry point
internal/
  config/           - Configuration, model map, API key management
  database/         - File-based JSON storage with mutex locks
  pool/             - Account pool with concurrency control
  upstream/         - Qwen API client, SSE consumer, payload builder
  services/         - ChatID pool, keepalive, auth, stream translators
  toolcall/         - Multi-format tool call parser (QNML/JSON/XML)
  api/              - HTTP handlers for all protocols
```

## License

MIT
