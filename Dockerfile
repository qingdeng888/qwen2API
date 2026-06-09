# syntax=docker/dockerfile:1.7

# ============================================================
# Stage 1: Build frontend assets
# ============================================================
FROM --platform=$BUILDPLATFORM node:20-bookworm-slim AS frontend-builder
WORKDIR /app
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ============================================================
# Stage 2: Build Go binary (multi-arch support)
# ============================================================
FROM --platform=$BUILDPLATFORM golang:1.22-bookworm AS go-builder
WORKDIR /src
COPY go.mod ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -trimpath -o /qwen2api ./cmd/server

# ============================================================
# Stage 3: Minimal runtime image
# ============================================================
FROM debian:bookworm-slim
WORKDIR /workspace

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

COPY --from=go-builder /qwen2api /usr/local/bin/qwen2api
COPY --from=frontend-builder /app/dist ./frontend/dist

# Create data directory (will be mounted as volume in production)
RUN mkdir -p /workspace/data /workspace/logs
# Provide default empty accounts file
RUN echo '[]' > /workspace/data/accounts.json

ENV PORT=7860
EXPOSE 7860

HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD curl -fsS "http://127.0.0.1:${PORT:-7860}/healthz" || exit 1

CMD ["qwen2api"]
