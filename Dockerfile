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
# STAGE 2: Final image
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

# Install faster-whisper (CPU version - much faster than whisper.cpp)
# tqdm for download progress bars
RUN pip install --no-cache-dir faster-whisper tqdm

# Disable HF XET protocol to get proper download progress
ENV HF_HUB_DISABLE_XET=1

# Clone and install llm-subtrans
RUN apt-get update && apt-get install -y --no-install-recommends git && \
    git clone https://github.com/machinewrapped/llm-subtrans.git /app/llm-subtrans --depth 1 && \
    cd /app/llm-subtrans && pip install --no-cache-dir -e ".[openai,gemini,claude]" && \
    apt-get purge -y git && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*

# Setup instructions directory (llm-subtrans expects /app/instructions/instructions.txt)
RUN ln -s /app/llm-subtrans/instructions /app/instructions

# Copy transcription script
COPY scripts/transcribe.py /app/scripts/transcribe.py

# Copy Go binary
COPY --from=go-builder /app/fusionn-muse .

# Create data directories and models cache
RUN mkdir -p /data/input /data/staging /data/processing /data/finished /data/subtitles /data/failed /app/models

ENV ENV=production
ENV CONFIG_PATH=/app/config/config.yaml


CMD ["./fusionn-muse"]
