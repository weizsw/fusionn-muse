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

# Install runtime dependencies (including fonts for subtitle rendering)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    curl \
    ca-certificates \
    tzdata \
    libgomp1 \
    fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/*

# Clone VideoCaptioner and install Python dependencies
# Based on VideoCaptioner pyproject.toml (excluding GUI packages like PyQt5)
RUN apt-get update && apt-get install -y --no-install-recommends git && \
    git clone https://github.com/WEIFENG2333/VideoCaptioner.git /app/videocaptioner --depth 1 && \
    pip install --no-cache-dir \
        faster-whisper \
        requests \
        openai \
        json-repair \
        diskcache \
        langdetect \
        tenacity \
        pydub \
        GPUtil \
        Pillow \
        fonttools && \
    apt-get purge -y git && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*

# Add VideoCaptioner to Python path
ENV PYTHONPATH="/app/videocaptioner"

# Copy scripts
COPY scripts/transcribe.py /app/scripts/transcribe.py
COPY scripts/subtitle_processor.py /app/scripts/subtitle_processor.py
COPY scripts/translate.py /app/scripts/translate.py

# Copy Go binary
COPY --from=go-builder /app/fusionn-muse .

# Create data directories and models cache
RUN mkdir -p /data/input /data/staging /data/processing /data/finished /data/subtitles /data/failed /app/models

ENV ENV=production
ENV CONFIG_PATH=/app/config/config.yaml

CMD ["./fusionn-muse"]
