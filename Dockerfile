# ════════════════════════════════════════════════════════════════════════════
# STAGE 1: Build Go binary
# ════════════════════════════════════════════════════════════════════════════
FROM golang:1.23-alpine AS go-builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X github.com/fusionn-muse/internal/version.Version=${VERSION}" \
    -o fusionn-muse ./cmd/fusionn-muse

# ════════════════════════════════════════════════════════════════════════════
# STAGE 2: Build whisper.cpp
# ════════════════════════════════════════════════════════════════════════════
FROM alpine:3.19 AS whisper-builder

RUN apk add --no-cache git cmake make g++ sdl2-dev

WORKDIR /build
RUN git clone https://github.com/ggerganov/whisper.cpp.git --depth 1

WORKDIR /build/whisper.cpp
RUN cmake -B build && \
    cmake --build build --config Release -j$(nproc)

# Download medium model (good balance of speed/quality)
RUN ./models/download-ggml-model.sh medium

# ════════════════════════════════════════════════════════════════════════════
# STAGE 3: Final image
# ════════════════════════════════════════════════════════════════════════════
FROM python:3.11-slim

WORKDIR /app

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    curl \
    ca-certificates \
    tzdata \
    && rm -rf /var/lib/apt/lists/*

# Copy whisper.cpp binary and model
COPY --from=whisper-builder /build/whisper.cpp/build/bin/main /usr/local/bin/whisper-cpp
COPY --from=whisper-builder /build/whisper.cpp/models/ggml-medium.bin /app/models/ggml-medium.bin

# Clone and setup llm-subtrans
RUN pip install --no-cache-dir \
    openai \
    anthropic \
    google-generativeai \
    httpx \
    srt \
    pysubs2 \
    regex \
    requests

RUN git clone https://github.com/machinewrapped/llm-subtrans.git /app/llm-subtrans --depth 1 || \
    (apt-get update && apt-get install -y git && \
     git clone https://github.com/machinewrapped/llm-subtrans.git /app/llm-subtrans --depth 1)

# Copy Go binary
COPY --from=go-builder /app/fusionn-muse .

# Create data directories
RUN mkdir -p /data/input /data/staging /data/processing /data/finished /data/subtitles /data/failed

ENV ENV=production
ENV CONFIG_PATH=/app/config/config.yaml

EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --retries=3 --start-period=10s \
    CMD curl -f http://localhost:8080/api/v1/health || exit 1

CMD ["./fusionn-muse"]
