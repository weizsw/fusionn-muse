# Fusionn-Muse

Automatic subtitle transcription and translation pipeline for qBittorrent. When a torrent completes, Fusionn-Muse durably accepts the media and processes it in the background. The recommended pipeline uses host MLX Qwen3 ASR and PySubtrans Custom Server translation.

## Features

- 🎤 **Host MLX Qwen3 ASR** - Transcription on an Apple Silicon host; no ASR models inside the container
- 🌐 **PySubtrans Translation** - OpenAI-compatible endpoints with explicit timeout and bounded retries
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

Copy the example config and customize the MLX host URL/path mapping and LLM settings (the example now selects `mlx_qwen3_asr`):

```bash
cp config/config.example.yaml config.yaml
```

### Key Settings

```yaml
pipeline:
  provider: "mlx_qwen3_asr"    # Configure the required host service below

translate:
  provider: "openai"          # openai, deepseek, siliconcloud, groq, etc.
  model: "gpt-4o-mini"
  api_key: "sk-..."
  target_lang: "Simplified Chinese"
```

### MLX Qwen3 ASR Pipeline

To run ASR on an Apple Silicon host and translate in the container:

```yaml
pipeline:
  provider: "mlx_qwen3_asr"

mlx_qwen3_asr:
  server_url: "http://host.docker.internal:8766"
  host_prefix: "/path/on/mac/that/maps-to-data"
  container_prefix: "/data"
  model: "Qwen/Qwen3-ASR-1.7B"
  language: ""
  timeout_minutes: 180

translate:
  provider: "custom"
  custom_server: "http://192.168.50.135:8317/v1"
  model: "gpt-5.4-mini"
  api_key: "sk-..."
  target_lang: "Simplified Chinese"
  instruction: "Optional PySubtrans --instruction text"
```

Run `host_asr_server.py` on macOS with `mlx-qwen3-asr` installed. The container calls its `/transcribe` endpoint; the host process maps `/data/...` to `host_prefix` and runs the MLX CLI locally.

This pipeline translates through PySubtrans's **Custom Server** client using the OpenAI-compatible `/chat/completions` endpoint. Keep `translate.custom_server` as the API base URL, including `/v1` or any reverse-proxy prefix. Requests are non-streaming, with a **180-second HTTP timeout and one retry by default**; there are no OpenAI SDK retries. The HTTP timeout measures inactivity, not total request duration. `llm_subtrans.timeout_minutes` remains the separate whole-file deadline. Python logs stream once at their original levels; translation failures report a short reason rather than replaying accumulated stderr, and deadlines/cancellation are distinguished from process failures.

`llm_subtrans.request_timeout_seconds` controls the HTTP timeout in seconds for each batch translation request (connect, read, write). It defaults to `180`; only positive integers are supported (PySubtrans 1.7.3 treats `0` as 300, so Muse rejects it). This is an HTTP socket/inactivity timeout per request; `llm_subtrans.timeout_minutes` remains the separate whole-file wall clock deadline.

`llm_subtrans.max_retries` controls retries after the initial request for a subtitle batch when it times out, cannot connect, or receives HTTP 5xx. It defaults to `1`; `5` allows up to six HTTP attempts per request, with PySubtrans's exponential backoff. Only positive integers are supported: PySubtrans 1.7.3 treats `0` as three retries, so Muse rejects it. Batch validation/retranslation can make additional requests; this setting is not a total request budget for a batch. `queue.max_retries` separately controls total whole-file Translation Attempts. The whole-file deadline remains in effect.

`llm_subtrans.retry_backoff_seconds` sets the base delay in seconds for batch retries. PySubtrans doubles this delay on each attempt (`delay * 2^retry`, starting at 3.0s, 6.0s, 12.0s...). Setting it to `0` disables waiting between retries (immediate retry), which is useful when the upstream LLM gateway or proxy manages its own fallback chain. It defaults to `3.0`; only finite non-negative numbers are allowed.

`llm_subtrans.min_batch_size` and `llm_subtrans.max_batch_size` configure the subtitle line bounds per translation batch (default `10` and `100`). Both must be positive integers, and `max_batch_size` must be greater than or equal to `min_batch_size`. Increasing these bounds reduces total HTTP requests and accelerates translation for models with large context windows.

```yaml
llm_subtrans:
  timeout_minutes: 180
  request_timeout_seconds: 300
  max_retries: 5
  retry_backoff_seconds: 0
  min_batch_size: 20
  max_batch_size: 100
```

Timeouts, retry counts, backoff, and batch size settings follow the Attempt's settings snapshot. Old snapshots without these options keep 180 seconds, one retry, 3.0 seconds backoff, and 10/100 batch bounds. To apply a new value to a failed execution, use `{"action":"retry","use_current_settings":true}`; completed Jobs use `retranslate` instead.

Optional `llm_subtrans.rate_limit` exposes PySubtrans's `--ratelimit` in **requests/minute (RPM)**. It accepts finite non-negative numbers; omitted or `0` preserves unpaced execution. For example:

```yaml
llm_subtrans:
  timeout_minutes: 180
  rate_limit: 80
```

This uses the actual Custom Server client: at 80 RPM each successful translation request takes at least 0.75 seconds, including its response time; slower requests need no extra wait. It is batch pacing, **not a strict global API quota**: HTTP retries inside a request and other proxy clients are not separately limited. It does not change `llm_subtrans.request_timeout_seconds` or resolve a stalled upstream response. The 1.7.3 Custom Server ignores a top-level `init_options(rate_limit=...)`; Muse passes it through the explicit provider/client settings instead.

Rate limits follow the Attempt's settings snapshot. Old snapshots without this option remain unpaced. To adopt a newly configured limit for a failed execution, explicitly select current settings with `{"action":"retry","use_current_settings":true}`; completed Jobs use `retranslate` instead.

### Container dependencies and retired pipeline

The image contains only the MLX host client, PySubtrans Custom Server translation, CPU OCR, and media preparation tools. It does not include VideoCaptioner, FasterWhisper, OpenAI SDK, GUI, or local ASR models. Translation/OCR dependencies are pinned in `requirements.txt`; update them deliberately and run the offline protocol tests before upgrading. Docker uses Go/pip build caches, and `.dockerignore` excludes configuration secrets, databases, models and local caches from the build context.

`pipeline.provider: videocaptioner` and the historical empty-provider fallback are **no longer executable**. Old configuration snapshots remain readable and are not silently switched to MLX. To recover an old task, configure `mlx_qwen3_asr` and explicitly select current settings in the unified action API; existing stage checkpoints remain intact. `whisper.*` and `translate.use_reflect`, `translate.threads`, `translate.batch_size` remain only as snapshot data and have no active consumer. Upgrading an installation/backup that still relies on VideoCaptioner requires this explicit migration; retaining an old image is the rollback option.

Build with `docker build --build-arg VERSION=<version> -t fusionn-muse:<version> .`. Rebuilding Go/scripts does not reinstall Python dependencies; changing `requirements.txt` does. No models or existing mounted data are deleted by the image change.

Verify the built image offline (the test scripts are mounted, not shipped):
```bash
docker run --rm --network none -e MUSE_IMAGE_TEST=1 \
  -e PYTHONPATH=/tests --entrypoint python3 -v "$PWD/scripts:/tests:ro" \
  fusionn-muse:<version> -m unittest -v test_logfmt test_llm_subtrans_translate \
  test_llm_subtrans_integration test_detect_hard_sub test_docker_runtime
```

See [config.example.yaml](config/config.example.yaml) for full documentation.

## Logging

Every application event is one physical line of plain text on the program's standard output stream — the Go service writes to stdout, the Python and script programs to stderr:

```text
2026-07-01T14:20:03.456+08:00 WARN component=processor event=translation_retry job_id=abc retry=1 retry_after_ms=2000 error="provider timeout" msg="Translation will retry"
```

Fields appear in this order: `component event`, then `job_id submission_id attempt_id request_id parent_job_id stage tool stream` when they apply, then the remaining fields sorted by name, and `msg` last. Timestamps are always UTC+8 with milliseconds. The complete rules are in [docs/design/unified-logging.md](docs/design/unified-logging.md).

`LOG_LEVEL` selects the level and defaults to `INFO`. In the Go service, the config key `log_level` overrides it without a restart: the service applies a change within 10 seconds of saving the file and logs one `log_level_changed` event. An empty `log_level` returns to `LOG_LEVEL`, and an invalid one keeps the current level. The Python scripts that the service starts keep using `LOG_LEVEL`. `ALIGN_LOG_LEVEL` is a Python and script compatibility alias, read by those programs only when `LOG_LEVEL` is unset; the Go service does not read it. An unknown value logs a visible `config_invalid` warning and falls back to `INFO`. Credentials are redacted at every level, and content excerpts appear only at `DEBUG`, in a `preview` field of at most 512 characters. The one exception is `detail`: when an external tool such as ffmpeg fails, the failure line (`job_failed`, `translation_retry`, `submission_failed` or `hard_sub_detection_failed`) carries up to three lines of the tool's standard error that look like error messages, capped at 300 characters.

### Where the logs go

- The container writes to stdout and stderr. Docker keeps them with `max-size: 20m` and `max-file: 6`, about 120 MiB per container, evicting the oldest data first. Read them with `docker compose logs -f` or the OrbStack container page. These options take effect when the container is recreated.
- Host-only programs are not part of the container log. `host_asr_server.py` and `host_server.py` write to stderr, and `scripts/torrent_complete.py` appends the same lines to `$LOG_FILE` (`/tmp/torrent-complete.log` by default). Redirect them to a file or your own collector when you need retention; this project does not rotate those files.
- Each run of `scripts/torrent_complete.py` sends an `X-Request-ID` header and writes the same `request_id` on all of its lines. When Muse accepts the request, the script also writes the returned `submission_id`. Search the container log for either value to find the Muse lines for that run.
- A panic while a Submission or Job is processed does not stop the service. That Submission or attempt fails through the normal failure path, and its `submission_failed`, `translation_retry` or `job_failed` line includes the stack.

## qBittorrent Setup

1. Copy `scripts/torrent_complete.py` to the qBittorrent host. It needs only Python 3.9 or later and its standard library. Set the constants at the top of the file for your deployment: `MUSE_URL`, `MUSE_CATEGORY` (the only category that is sent), and `HOST_INPUT_PATH` and `CONTAINER_INPUT_PATH` (the download mount, and the path that Muse sees for it). You can also set `MUSE_URL`, `LOG_FILE` and `LOG_LEVEL` as environment variables.

2. Go to **Options → Downloads → Run external program on torrent completion** and add the command:

   ```bash
   python3 /path/to/torrent_complete.py "%N" "%L" "%I" "%F"
   ```

   The script sends one request, does not follow redirects, and stops waiting after 15 seconds. Only **HTTP 202** with a `submission_id` counts as success. A torrent in another category is only logged. A path outside the download mount, a failed request or a missing acknowledgement is logged as `webhook_failed`, and the script exits with status 1.

A `202` receipt includes `submission_id` and means the input and pending preparation have committed to SQLite—not that media preparation or subtitle processing succeeded. Scanning, ISO extraction and multipart conversion run in the background with a three-hour preparation deadline. Poll the Submission detail for its outcome and linked Job.

When Muse is unavailable or cannot persist the request, the script logs failure and exits nonzero. It does not automatically resend: resubmit missed completions manually. If the response was lost after commit, resubmission may create another Submission receipt, but resolving the same Media item still links to one Job without restarting its work.

## Folder Structure

```
/data/
├── torrents/              # Input: torrent download folder (read-only)
└── automation/
    ├── staging/           # Queue: waiting for processing
    ├── processing/        # Active: currently being processed
    ├── scraping/          # Output: videos ready for media server
    ├── subtitles/         # Output: translated .srt files
    ├── transcriptions/    # Durable source subtitles for retranslation
    ├── meta/fusionn-muse.db # Durable SQLite job state
    └── failed/            # Failed jobs (manual inspection)
```

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/webhook/torrent` | Persist a Submission and return HTTP 202 before preparation |
| GET | `/api/v1/tasks` | Lightweight cursor-paginated observation of all work |
| GET | `/api/v1/tasks/stats` | SQL aggregation using the same observation/filter rules |
| GET | `/api/v1/tasks/:kind/:id` | Current detail, available actions and a bounded history page |
| POST | `/api/v1/tasks/:kind/:id/actions` | Explicit retry, resume or retranslation |
| POST | `/api/v1/tasks/actions` | Retry all failed tasks or resume all interrupted tasks |
| GET | `/api/v1/health` | Liveness check |
| GET | `/api/v1/version` | Version info |

**Breaking change:** `/queue`, `/jobs` (including events), `/submissions`, `/files` and `/retry` routes under `/api/v1` are removed, not redirected. Their data is retained. Change clients to the routes above; submit untracked files through the webhook rather than a folder-wide retry. The webhook returns `submission_id` and a `Location` under `/tasks/submission/`.

### Unified list, statistics and detail

```text
GET /api/v1/tasks?limit=50&status=failed
GET /api/v1/tasks/stats
GET /api/v1/tasks/job/SNOS-411?limit=50
GET /api/v1/tasks/job/<job-id>?limit=50
GET /api/v1/tasks/submission/<submission-id>?limit=50
```

For `job` detail and actions, the path reference accepts either the Job ID or a normalized **Video code** such as `SNOS-411` (case-insensitive). You do not need to list Jobs to find an ID:

```bash
curl 'http://localhost:4444/api/v1/tasks/job/SNOS-411'
curl -X POST 'http://localhost:4444/api/v1/tasks/job/SNOS-411/actions' \
  -H 'Content-Type: application/json' -d '{"action":"retranslate"}'
```

Check `allowed_actions` first: use `retry` for failure, `resume` for interruption, or `retranslate` with an eligible persisted source subtitle. Code lookup uses the original media's normalized source key, so an explicit output-name repair does not change its code. A missing code returns 404; multiple Jobs sharing one code return **409** with `code: ambiguous_job_reference`, `reference`, and lightweight `candidates` containing `kind`, `id`, `name`, `source_path`, and latest `status`. Choose a candidate ID; no ambiguous action starts an execution. IDs, media identity and stored history are unchanged, and unparsed Submissions still require their Submission ID. Successful detail/action responses retain the canonical Job ID; an accepted action's `Location` uses that ID. History cursors work across code and ID references to the same Job. This adds no routes or list/search filters and does not change the database schema.

The list returns `tasks` and optional `next_cursor`; its default limit is 50, maximum 200. Pass the cursor with the same `kind` (`job` or `submission`) and `status` filters. Ordering is newest creation first, with deterministic tie-breaks. Pagination is a live view, not a frozen export: refresh to observe work that resolves or changes status during traversal. Lists never include settings snapshots or history.

An unresolved Submission occupies one row. Once resolved, its Job replaces it: multiple notifications for one Media item count as **one Job**, including legacy Jobs without a Submission. The original Submission ID still works for detail; a resolved receipt exposes `canonical_task` and the Job ID. Resolved receipt detail can show raw `resolved` state, but that receipt is not counted again in the list/statistics.

| List `status` | Meaning |
|---------------|---------|
| `pending` | Waiting for a preparation or processing stage |
| `processing` | Running preparation or processing; inspect `stage` |
| `completed` | Latest execution completed |
| `failed` | Latest execution failed; inspect `error` |
| `interrupted` | Running work stopped; explicit resume required |
| `ignored` | No eligible Media item; preparation reason recorded |

### Bulk retry and resume

`POST /api/v1/tasks/actions` applies one recovery action to all tasks in its matching latest execution state, including Jobs and unresolved Submissions:

```bash
# Resume all interrupted tasks with the current configuration.
curl -X POST 'http://localhost:4444/api/v1/tasks/actions' \
  -H 'Content-Type: application/json' \
  -d '{"action":"resume","use_current_settings":true}'

# Retry all failed tasks with the current configuration.
curl -X POST 'http://localhost:4444/api/v1/tasks/actions' \
  -H 'Content-Type: application/json' \
  -d '{"action":"retry","use_current_settings":true}'
```

Only `retry` and `resume` are supported. `retry` selects `failed`; `resume` selects `interrupted`. Failed/interrupted Retranslations are included while preserving the Job's completed result. `ignored`, completed, pending and processing tasks are excluded. Bulk actions do not support `output_name` or `retranslate`; use the individual action endpoint for those.

Without `use_current_settings`, each recovery keeps its prior execution's settings. With it, Muse reloads and validates configuration once, then uses one current settings snapshot for the entire batch. Invalid configuration returns HTTP 400 before any recovery is admitted.

Targets are captured before any recovery starts, without the list API's 200-row page limit. Each task is admitted independently using the single-task action rules. If another request has already replaced a selected execution, that item reports an error instead of recovering the newer execution. Tasks that become failed/interrupted after selection require another bulk call. A failure on one item does not roll back successful admissions or prevent the other items from being attempted.

HTTP 202 reports admission results, not completed processing:

```json
{
  "action": "resume",
  "matched": 2,
  "accepted": 1,
  "failed": 1,
  "results": [
    {"kind": "job", "id": "job-id", "execution_id": 208},
    {"kind": "submission", "id": "submission-id", "error": "recovery error"}
  ]
}
```

`failed` counts items that could not be admitted; inspect their `error`. No matching tasks returns HTTP 202 with zero counts and an empty `results` array. Queue/storage failure before admission returns HTTP 503. Query `/api/v1/tasks?status=pending`, `/api/v1/tasks?status=processing` or `/api/v1/tasks/stats` to follow execution after admission.

`status` describes the **latest execution**. `job_status` describes the retained Job outcome: failed/interrupted retranslation can have `job_status: completed`, since the previous result remains valid. Failed and interrupted are counted separately. Statistics return `total` and `statuses` using exactly the same row/status rules; optional `kind`/`status` filters work in both. Separate HTTP calls can naturally differ while work changes.

Detail returns `allowed_actions`, `can_change_output_name`, `history`, and optional `next_history_cursor`. Use `history_cursor` to continue; default history limit is 50, maximum 200. Job history combines its Attempts and linked Preparation runs, including each run's immutable input and non-secret snapshot. Action history records requested configuration selection and any output-name change. Database read failures return 503, not empty lists, zero statistics or fake 404s.

### Explicit recovery actions

```json
{"action":"retry"}
```

- `retry`: failed/ignored preparation or a failed Job execution, including failed retranslation.
- `resume`: interrupted preparation or Job execution, including interrupted retranslation.
- `retranslate`: an eligible terminal Job with a valid persisted source subtitle. It does not require a completed video's continued presence.

Default configuration comes from **the execution being retried/resumed**, not the first-ever submission. `retranslate` likewise retains the latest recorded configuration unless selected otherwise. Add `"use_current_settings":true` to reload, validate and snapshot current non-secret configuration; credentials always come from current runtime values. Invalid current configuration creates no new execution. A changed profile bypasses prior translation reuse. Identical action/profile requests reuse an active execution; conflicting actions/profiles return 409 rather than silently applying a different action.

The action body is required and has only `action`, optional `use_current_settings`, and optional `output_name`. Success returns HTTP 202 with the existing task identity and its Attempt/Preparation run. Invalid JSON/fields/actions return 400, missing identity 404, incompatible state or artifact conflict 409, and storage/service unavailability 503. Conflict responses include available actions when detail can be read. Correct a Submission's wrong path/name by submitting new input, never editing its history; use its canonical Job for post-resolution actions.

### Manual repair of naming conflicts

For a failed/interrupted Job **before a complete Transcription exists**, append a safe new basename with the same video extension:

```text
POST /api/v1/tasks/job/<job-id-or-code>/actions
```
```json
{"action":"retry","output_name":"SNOS-411-alt.mp4"}
```

For an interruption use `resume` instead. This reserves the new output paths, records the prior name and restarts preparation under the same Job ID and Media identity. Existing/ambiguous ownership still blocks the new name; media must be present and match its signature. It does not relocate completed source subtitles or remove another Job's claims. Existing old claims are retained conservatively. Do not delete files or alter SQLite to release ownership: resubmitting the same media still resolves to its original Job. Repair missing input at a recorded/known recovery location before retry; arbitrary manual file renaming is not an identity recovery mechanism.

Queued work continues after restart; running work becomes interrupted and requires explicit recovery. Preparation caches are isolated by Submission/run and never delete submitted downloads. Undelivered qB notifications require manual resubmission; there is no Redis/outbox. Only one Muse instance may use the database.

The default database is `/data/automation/meta/fusionn-muse.db`, matching the other automation directories (`/Volumes/External/automation/meta/fusionn-muse.db` for `/Volumes/External:/data`). Explicit `queue.db_path` values remain supported. **Path-change safety:** this is a default, not automatic relocation or merging. Before upgrading an installation that omitted `db_path` and used the former `/data/meta` default, explicitly set its existing path or stop Muse and migrate a consistent backup. If both paths contain databases, preserve both histories and resolve identity/path conflicts before switching; simply changing the setting can expose an old or empty database. A database path change requires restarting Muse.

**Upgrade safety:** schema version is now **5**, retaining version-2/3/4 records and adding Attempt action/receipt metadata. Stop Muse before copying SQLite files (including WAL), or use a consistent SQLite backup. Older binaries reject the upgraded database; rollback requires compatible code or restoring the pre-upgrade backup while stopped.

### Stage recovery safety

- Checkpoint, lane-handoff and final-outcome commits retry without redoing finished external work. A live `persistence_error` identifies blocked storage writes; shutdown interrupts the Attempt for explicit recovery.
- Recovery checks media identity in recorded and known automation locations. Missing/empty files, directories, symlinks and conflicting output ownership fail explicitly, without automatic model retries or silent retranscription.
- Canonical paths belong to a Job. Another Media item with the same output name is blocked, not renamed automatically or overwritten. Historical ambiguous ownership also blocks reuse. Deleting a file alone does not release another Job's ownership claim.
- Only a complete Translation becomes a durable Attempt receipt. Resume reuses it when its saved source matches the editable SRT; source edits require a new Translation. Failed Retranslation preserves the completed Job's canonical subtitle and result path, even when its video has been consumed downstream.
- A strong kill before an external response is durably saved may require another request on recovery. The service does not promise exactly-once ASR/LLM execution or survival of disk loss/corruption. Keep database and media backups; the storage backend must honor synchronization.

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

Ordered multipart videos such as `ABC-001A.wmv`, `ABC-001B.wmv`, or `abc00001hhb1.wmv`, `abc00001hhb2.wmv` are assembled into one staged video before processing, preferring `.mkv` and falling back to `.mp4` when transcode is needed. Playable disc/archive image sources such as `.iso`, `.nrg`, `.img`, `.mdf`, and `.bin` are extracted without Docker loop mounts and remuxed to `.mkv` when possible.

### Chinese Subtitle Detection

Files are skipped when Fusionn-Muse finds existing Chinese subtitles:

- Filename indicators: `-C`, `_C`, `.C`, `zh`, `chs`, `cht`, `chi`, `cn`, `gb`, `big5`, `sc`, `tc`, `中文`, `简中`, `繁中`, `软中`, `硬中`, `字幕`, `内嵌`, `内封`, `中字`, `国语`, `双语`
- Sidecar subtitles: `.srt`, `.ass`, `.ssa`, `.vtt` matched by same basename or video code, with Chinese filename/content hints
- Embedded subtitle streams: `ffprobe` subtitle metadata with Chinese language/title hints
- Hard subtitles: optional RapidOCR check for sufficient Chinese subtitle coverage; it samples long videos after minute 30 and short videos from their midpoint, requiring CJK hits across multiple time regions and excluding text lines containing Japanese kana

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
│   ├── executor/           # Host ASR / PySubtrans adapters
│   ├── queue/              # Job queue
│   ├── fileops/            # File operations
│   └── client/apprise/     # Notification client
├── pkg/logger/             # Logging utilities
├── scripts/                # Python scripts
│   ├── llm_subtrans_translate.py # PySubtrans CLI
│   ├── detect_hard_sub.py  # CPU OCR detection
│   └── test_*.py           # Protocol, OCR, image and webhook tests
└── config/
    └── config.example.yaml # Example configuration
```
