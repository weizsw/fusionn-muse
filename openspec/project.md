# Project Context

## Purpose
Fusionn-Muse is a subtitle automation pipeline that automatically transcribes and translates video files when torrents complete downloading. It provides a webhook API for qBittorrent integration, processing videos through FasterWhisper transcription and LLM-based translation (using VideoCaptioner's core modules).

**Core flow:**
1. qBittorrent triggers webhook on torrent completion
2. Video files are staged and queued for processing
3. FasterWhisper transcribes audio → SRT subtitles
4. LLM translates subtitles to target language
5. Video moves to scraping folder, subtitles to subtitles folder
6. Notifications sent via Apprise

## Tech Stack
- **Go 1.23** - Main application, HTTP server (Gin), job queue
- **Python 3.11** - Transcription (faster-whisper) and translation scripts
- **VideoCaptioner** - Cloned for LLM translation modules (`app/core/translate/`, `app/core/llm/`)
- **Docker** - Multi-stage build (Go builder → Python runtime with ffmpeg)
- **Viper** - Configuration management with YAML + env vars + hot-reload

### Key Dependencies
- `github.com/gin-gonic/gin` - HTTP router
- `github.com/spf13/viper` - Configuration
- `go.uber.org/zap` - Logging
- `faster-whisper` - Python ASR engine
- `openai` - LLM API client (OpenAI-compatible)

## Project Conventions

### Code Style
- Go: Follow Uber Go Style Guide, Go Code Review Comments, Effective Go
- Use structured logging via `pkg/logger` (zap wrapper)
- Emoji prefixes in logs for visual scanning (📥, ✅, ❌, ⚠️, 🎤, 🌐)
- Config structs use `mapstructure` tags for Viper binding
- Python scripts: Standalone CLI tools in `/app/scripts/`

### Architecture Patterns
- **Layered architecture:**
  - `cmd/` - Entry point
  - `internal/handler/` - HTTP handlers (Gin)
  - `internal/service/processor/` - Business logic (pipeline orchestration)
  - `internal/executor/` - External process execution (whisper, translator)
  - `internal/queue/` - Sequential job queue with retry logic
  - `internal/config/` - Configuration management with hot-reload
  - `internal/fileops/` - File operations (hardlink, move, cleanup)
  - `pkg/logger/` - Shared logging

- **Processor interface:** `queue.Processor` defines `Process(ctx, job) error`
- **Executor pattern:** Go wraps Python scripts via `os/exec`, streaming output
- **Hot-reload config:** Manager polls file changes, triggers callbacks
- **Folder-based state machine:**
  - `staging/` → `processing/` → `scraping/` (success) or `failed/` (error)

### Testing Strategy
- Unit tests for fileops, config parsing
- Integration tests require Docker (external dependencies: faster-whisper, LLM APIs)
- Manual testing via `/api/v1/retry/staging` endpoints

### Git Workflow
- Main branch: `dev` (feature development)
- Conventional commits not enforced, but descriptive commit messages expected
- PRs for significant changes

## Domain Context
- **Subtitle formats:** SRT only (industry standard)
- **Language codes:** Following ISO 639-1 (en, zh, ja, ko, etc.)
- **Embedded subtitle detection:** Files with `-C` suffix skip transcription/translation
- **Filename cleaning:** Removes `-C` suffixes and normalizes names
- **CJK handling:** Different line length limits for CJK (25 chars) vs Latin (18 words)
- **LLM providers:** OpenAI-compatible API (OpenAI, DeepSeek, Groq, Together, etc.)

## Important Constraints
- **Sequential processing:** One job at a time (GPU memory, API rate limits)
- **Docker-first:** Hardcoded folder paths assume Docker volume mounts
- **VideoCaptioner dependency:** Translation uses cloned repo at build time
- **ffmpeg required:** For audio extraction in transcription
- **No persistence:** Job queue is in-memory (lost on restart)

## External Dependencies
- **qBittorrent** - Triggers webhooks on torrent completion
- **faster-whisper** - Local ASR (requires CUDA for GPU acceleration)
- **LLM APIs** - OpenAI, DeepSeek, Groq, SiliconCloud, etc. (OpenAI-compatible)
- **Apprise** - Optional notification service (Telegram, Discord, email, etc.)
- **VideoCaptioner** - GitHub repo cloned for translation modules
