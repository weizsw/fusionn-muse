# syntax=docker/dockerfile:1
FROM golang:1.26.0-alpine AS go-builder

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/fusionn-muse/internal/version.Version=${VERSION}" \
    -o /fusionn-muse ./cmd/fusionn-muse

FROM python:3.11-slim-trixie

WORKDIR /app
ENV ENV=production \
    CONFIG_PATH=/app/config/config.yaml \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

# Media preparation and CPU OCR; ASR runs on the MLX host.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg ca-certificates tzdata libgomp1 libarchive-tools 7zip nrg2iso \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt ./
RUN --mount=type=cache,target=/root/.cache/pip \
    python -m pip install --no-compile -r requirements.txt && python -m pip check

COPY scripts/logfmt.py scripts/llm_subtrans_translate.py scripts/detect_hard_sub.py ./scripts/
COPY --from=go-builder /fusionn-muse ./fusionn-muse

CMD ["./fusionn-muse"]
