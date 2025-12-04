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
# STAGE 2: Download faster-whisper-xxl
# ════════════════════════════════════════════════════════════════════════════
FROM debian:bookworm-slim AS whisper-downloader

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    ca-certificates \
    p7zip-full \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /whisper

# Download and extract faster-whisper-xxl (Linux version)
RUN curl -L -o whisper.7z \
    "https://github.com/Purfview/whisper-standalone-win/releases/download/Faster-Whisper-XXL/Faster-Whisper-XXL_r245.4_linux.7z" && \
    7z x whisper.7z && \
    rm whisper.7z && \
    chmod +x Faster-Whisper-XXL/faster-whisper-xxl

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
    libgomp1 \
    && rm -rf /var/lib/apt/lists/*

# Copy faster-whisper-xxl from downloader stage
COPY --from=whisper-downloader /whisper/Faster-Whisper-XXL /app/faster-whisper-xxl
ENV PATH="/app/faster-whisper-xxl:${PATH}"

# Clone llm-subtrans (for translation)
RUN apt-get update && apt-get install -y --no-install-recommends git && \
    git clone https://github.com/machinewrapped/llm-subtrans.git /app/llm-subtrans --depth 1 && \
    cd /app/llm-subtrans && pip install --no-cache-dir -e ".[openai,gemini,claude]" && \
    apt-get purge -y git && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*

# Clone VideoCaptioner (use its core modules for subtitle processing)
RUN apt-get update && apt-get install -y --no-install-recommends git && \
    git clone https://github.com/WEIFENG2333/VideoCaptioner.git /app/videocaptioner --depth 1 && \
    pip install --no-cache-dir openai json-repair diskcache langdetect tenacity && \
    apt-get purge -y git && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*

# Add VideoCaptioner to Python path
ENV PYTHONPATH="/app/videocaptioner:${PYTHONPATH}"

# Setup instructions directory (llm-subtrans expects /app/instructions/instructions.txt)
RUN ln -s /app/llm-subtrans/instructions /app/instructions

# Copy scripts
COPY scripts/transcribe.py /app/scripts/transcribe.py
COPY scripts/subtitle_processor.py /app/scripts/subtitle_processor.py

# Copy Go binary
COPY --from=go-builder /app/fusionn-muse .

# Create data directories and models cache
RUN mkdir -p /data/input /data/staging /data/processing /data/finished /data/subtitles /data/failed /app/models

ENV ENV=production
ENV CONFIG_PATH=/app/config/config.yaml

CMD ["./fusionn-muse"]
