# syntax=docker/dockerfile:1.7

# ============================================================
# Stage 1: Build Go binary (multi-arch support)
# ============================================================
FROM --platform=$BUILDPLATFORM golang:1.22-bookworm AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -trimpath -o /qwen2api ./cmd/server

# ============================================================
# Stage 2: Minimal runtime image
# ============================================================
FROM debian:bookworm-slim
WORKDIR /workspace

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /qwen2api /usr/local/bin/qwen2api
COPY data/ ./data/

RUN mkdir -p /workspace/data /workspace/logs

ENV PORT=7860
EXPOSE 7860

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -fsS "http://127.0.0.1:${PORT:-7860}/healthz" || exit 1

CMD ["qwen2api"]
