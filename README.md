# Fusionn-Muse

Automatic subtitle transcription and translation pipeline for qBittorrent. When a torrent completes, Fusionn-Muse transcribes the audio using FasterWhisper and translates subtitles to your target language using LLM APIs.

## Features

- 🎤 **FasterWhisper Transcription** - GPU-accelerated speech-to-text with hallucination filtering
- 🌐 **LLM Translation** - OpenAI-compatible APIs (OpenAI, DeepSeek, Groq, SiliconCloud, etc.)
- 📥 **qBittorrent Integration** - Webhook triggers processing on torrent completion
- 🔄 **Hot-Reload Config** - Change settings without restarting
- 📁 **Smart File Detection** - Filters ads/samples by size and filename patterns
- 🔔 **Notifications** - Apprise integration (Telegram, Discord, email, etc.)

## Quick Start

### Docker (Recommended)

```bash
docker run -d \
  -p 8080:8080 \
  -v /path/to/downloads:/data/torrents:ro \
  -v /path/to/automation:/data/automation \
  -v ./config.yaml:/app/config/config.yaml:ro \
  ghcr.io/your-username/fusionn-muse:latest
```

### Docker Compose

```yaml
services:
  fusionn-muse:
    image: ghcr.io/your-username/fusionn-muse:latest
    ports:
      - "8080:8080"
    volumes:
      - /path/to/downloads:/data/torrents:ro
      - /path/to/automation:/data/automation
      - ./config.yaml:/app/config/config.yaml:ro
    environment:
      - ENV=production
    restart: unless-stopped
```

## Configuration

Copy the example config and customize:

```bash
cp config/config.example.yaml config.yaml
```

### Key Settings

```yaml
whisper:
  model: "large-v2"           # tiny, base, small, medium, large-v2, large-v3
  language: "ja"              # Source language hint (or "" for auto-detect)
  device: "auto"              # cuda, cpu, or auto

translate:
  provider: "openai"          # openai, deepseek, siliconcloud, groq, etc.
  model: "gpt-4o-mini"
  api_key: "sk-..."
  target_lang: "Simplified Chinese"
```

See [config.example.yaml](config/config.example.yaml) for full documentation.

## qBittorrent Setup

1. Go to **Options → Downloads → Run external program on torrent completion**

2. Add the command:

   ```bash
   curl -X POST http://fusionn-muse:8080/api/v1/webhook/torrent \
     -H "Content-Type: application/json" \
     -d '{"path": "%F", "name": "%N", "category": "%L"}'
   ```

   Or use the provided script: `scripts/torrent-complete.sh`

## Folder Structure

```
/data/
├── torrents/              # Input: torrent download folder (read-only)
└── automation/
    ├── staging/           # Queue: waiting for processing
    ├── processing/        # Active: currently being processed
    ├── scraping/          # Output: videos ready for media server
    ├── subtitles/         # Output: translated .srt files
    └── failed/            # Failed jobs (manual inspection)
```

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/webhook/torrent` | qBittorrent completion callback |
| GET | `/api/v1/queue` | List all jobs |
| GET | `/api/v1/queue/stats` | Queue statistics |
| GET | `/api/v1/queue/:id` | Get job details |
| POST | `/api/v1/retry/staging` | Re-queue all staging files |
| POST | `/api/v1/retry/failed` | Re-queue all failed files |
| POST | `/api/v1/retry/failed/:name` | Re-queue specific failed file |
| GET | `/api/v1/files/staging` | List staging files |
| GET | `/api/v1/files/failed` | List failed files |
| GET | `/api/v1/health` | Health check |
| GET | `/api/v1/version` | Version info |

## Video File Filtering

When processing a folder, Fusionn-Muse automatically filters out ads and samples:

- **Size filter**: Files ≤200MB are skipped
- **Code detection**: Hyphenated and compact filenames are supported
- **Fallback detection**: If a filename has no usable code, the folder name is checked next, then the torrent name
- **Selection**: Largest valid file is selected when no ordered multipart set is present

Fusionn-Muse detects codes from both hyphenated and compact filenames:

- `SSNI-083.mp4` -> `SSNI-083`
- `ssni00083hhb.mp4` -> `SSNI-083`
- `pppd176A.FHD.wmv` -> `PPPD-176`

Ordered multipart videos such as `ABC-001A.wmv`, `ABC-001B.wmv`, or `abc00001hhb1.wmv`, `abc00001hhb2.wmv` are assembled into one `.mkv` before processing. Playable disc/archive image sources such as `.iso`, `.nrg`, `.img`, `.mdf`, and `.bin` are extracted without Docker loop mounts and remuxed to `.mkv` when possible.

### Chinese Subtitle Detection

Files with Chinese subtitle indicators are skipped (already have subtitles):

- Suffixes: `-C`, `_C`, `.C`
- Language codes: `zh`, `chs`, `cht`, `chi`, `cn`, `gb`, `big5`, `sc`, `tc`
- Chinese terms: `中文`, `简中`, `繁中`, `软中`, `硬中`, `字幕`, `内嵌`, `内封`, `中字`, `国语`, `双语`

## LLM Providers

All providers use OpenAI-compatible API:

| Provider | Base URL | Models |
|----------|----------|--------|
| `openai` | api.openai.com | gpt-4o, gpt-4o-mini |
| `deepseek` | api.deepseek.com | deepseek-chat, deepseek-reasoner |
| `siliconcloud` | api.siliconflow.cn | Qwen, Yi, DeepSeek |
| `groq` | api.groq.com | llama, mixtral |
| `openrouter` | openrouter.ai | Many models |
| `together` | api.together.xyz | Various |
| `fireworks` | api.fireworks.ai | Various |
| `custom` | Your endpoint | Any OpenAI-compatible |

## Development

### Prerequisites

- Go 1.23+
- Python 3.11+ (for transcription/translation scripts)
- Docker (for building images)

### Build

```bash
# Build binary
make build

# Run locally
make run

# Run tests
make test

# Build Docker image
make docker
```

### Project Structure

```
fusionn-muse/
├── cmd/fusionn-muse/       # Entry point
├── internal/
│   ├── config/             # Configuration management
│   ├── handler/            # HTTP handlers (Gin)
│   ├── service/processor/  # Processing pipeline
│   ├── executor/           # Whisper & translator wrappers
│   ├── queue/              # Job queue
│   ├── fileops/            # File operations
│   └── client/apprise/     # Notification client
├── pkg/logger/             # Logging utilities
├── scripts/                # Python scripts
│   ├── transcribe.py       # FasterWhisper transcription
│   ├── translate.py        # LLM translation
│   └── subtitle_processor.py # Post-processing
└── config/
    └── config.example.yaml # Example configuration
```
