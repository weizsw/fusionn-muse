# Linux 部署与架构说明

本文档说明 **Fusionn-Muse** 在 Linux 操作系统下的当前部署方式、运行状态及后续转录方案适配规划。

---

## 一、平台支持现状客观说明

| 模块 / 环节 | Linux 运行状态 | 说明 |
|:---|:---:|:---|
| **Muse 核心服务** (Go + 任务队列调度) |  完全原生支持 | 标准 Docker 容器化运行，支持 x86_64 与 arm64 |
| **媒体扫描与过滤** (广告过滤/番号提取/多段合并/ISO提取) |  完全原生支持 | 基于容器内置的 ffmpeg 与 ffprobe，无宿主平台依赖 |
| **中文字幕智能检测** (文件名/外挂/内嵌字幕流) |  完全原生支持 | 纯文本与 ffprobe 媒体流元数据检测 |
| **硬字幕 OCR 覆盖率判定** (RapidOCR) |  完全原生支持 | 基于 CPU ONNX Runtime，容器内执行，用于检测是否已有硬字幕 |
| **LLM 字幕翻译引擎** (PySubtrans) |  完全原生支持 | 容器内向外部 OpenAI 兼容端点发起 HTTP 请求 |
| **下游影视联动** (mdc-ng / 115strm / Emby) |  完全原生支持 | 标准目录监控与文件流转 |
| **本地语音转录 (ASR)** | ⚠️ **专项开发进行中** | 当前生产实测方案基于 macOS Apple Silicon 的 MLX 框架。Linux 平台（首批覆盖 NVIDIA GPU 与纯 CPU）转录适配正在专项推进中。 |

> **特别说明（重要机制）**：
> 1. **硬字幕 OCR 的真实行为**：内置的 RapidOCR 仅用于抽样检测视频是否已压制充分的中文字幕。**一旦检测命中，Muse 会判定视频已有中文字幕，直接跳过转录与翻译，将视频交付至 `scraping` 目录供下游刮削**；OCR 模块**并不提取文字内容进行翻译**。
> 2. **转录依赖**：对于无中文字幕的生肉视频，Muse 必须调用 ASR 转录服务。当前内置客户端 `mlx_qwen3_asr` 调用的是专有的 `/transcribe` 文件路径协议（由 macOS 宿主机 `host_asr_server.py` 实现），不兼容 OpenAI Whisper API。因此在当前版本中，缺少中文字幕的视频在 Linux 下尚无法在本地完成转录。

---

## 二、当前 Linux 用户的部署与使用方式

在当前版本中，Linux 用户可使用 Muse 运行**媒体准备、广告过滤、多段合并、ISO提取，以及已压制中文字幕的智能识别与自动放行流转**。

### 1. 目录架构规划

假设 Linux 宿主机存储路径为 `/mnt/storage`：

```text
/mnt/storage/
├── torrents/                  # qBittorrent 下载目录
│   └── whisparr/
├── automation/
│   ├── staging/
│   ├── processing/
│   ├── scraping/              # 输出至 mdc-ng 监控目录
│   ├── finished/              # mdc-ng 整理归档目录
│   ├── subtitles/             # 中文字幕库
│   ├── transcriptions/        # 原始字幕归档
│   ├── meta/                  # SQLite 数据库
│   └── failed/                # 故障归档
└── strm/
    └── adults/                # Emby 媒体库目录
```

在终端创建目录结构：

```bash
STORAGE_ROOT="/mnt/storage"  # 替换为你的实际物理存储根路径

mkdir -p "${STORAGE_ROOT}/torrents/whisparr"
mkdir -p "${STORAGE_ROOT}/automation/"{staging,processing,scraping,finished,subtitles,transcriptions,meta,failed}
mkdir -p "${STORAGE_ROOT}/strm/adults"
```

### 2. Linux Docker Compose 配置

创建 `docker-compose.yml`：

```yaml
services:
  fusionn-muse:
    image: ghcr.io/weizsw/fusionn-muse:latest
    container_name: fusionn-muse
    restart: unless-stopped
    stop_grace_period: 45s
    ports:
      - "4444:8080"
    volumes:
      - ./config.yaml:/app/config/config.yaml:ro
      - /mnt/storage:/data
    environment:
      - ENV=production
      - TZ=Asia/Shanghai
      - PUID=1000
      - PGID=1000
      - LOG_LEVEL=info
    logging:
      driver: json-file
      options:
        max-size: "20m"
        max-file: "6"
```

### 3. 配置 `config.yaml`

```yaml
server:
  port: 8080

pipeline:
  provider: "mlx_qwen3_asr"

mlx_qwen3_asr:
  server_url: "http://host.docker.internal:8766"
  host_prefix: "/mnt/storage"
  container_prefix: "/data"
  model: "Qwen/Qwen3-ASR-1.7B"
  language: "ja"
  timeout_minutes: 180

translate:
  provider: "openai"              # 支持 openai, deepseek, siliconcloud, custom 等
  model: "gpt-4o-mini"
  api_key: "sk-your-api-key"
  target_lang: "Simplified Chinese"

llm_subtrans:
  timeout_minutes: 180
  rate_limit: 0
  max_retries: 5
  request_timeout_seconds: 180
  retry_backoff_seconds: 3.0
  min_batch_size: 10
  max_batch_size: 100

# 开启硬字幕检测（关键：自动识别已有中文字幕并直接放行交付至 scraping）
hard_sub_ocr:
  enabled: true

queue:
  db_path: "/data/automation/meta/fusionn-muse.db"
  max_retries: 3
  retry_delay_ms: 10000
```

> **注意**：若处理的媒体未检出中文字幕，流水线将尝试请求 `mlx_qwen3_asr.server_url` 进行转录。因当前 Linux 尚未搭载该服务，生肉视频任务会报错并等待后续 Linux ASR 模块。

### 4. 启动服务与连通性验证

```bash
docker compose up -d
```

在仓库根目录设置 `MUSE_URL`（例如 `http://127.0.0.1:4444`），调用与 macOS、agent 共用的检查命令：

<!-- muse-verification-example -->
```bash
python3 scripts/verify_deployment.py health --url "${MUSE_URL:?请设置 Muse 地址}"
```

对独立部署中的已有中文字幕样本，设置 `MEDIA_ROOT`、`SAMPLE_VIDEO`、`SAMPLE_NAME` 和等待秒数 `VERIFY_TIMEOUT`，检查放行交付：

<!-- muse-verification-example -->
```bash
python3 scripts/verify_deployment.py run \
  --url "${MUSE_URL:?}" --host-root "${MEDIA_ROOT:?}" --container-root /data \
  --sample "${SAMPLE_VIDEO:?}" --name "${SAMPLE_NAME:?}" --category whisparr \
  --expect delivered --timeout "${VERIFY_TIMEOUT:?}"
```

这是有副作用的样本投递，请使用用户授权的隔离目录。结果只表示关联任务的交付产物已核对，不证明 Linux ASR 或 LLM 本次实际调用。NVIDIA 与 CPU 仍需各自完成真实推理验收；本轮没有启动 Linux 服务。完整结果解释与验收要求见[部署检查命令](verification.md)。

---

## 三、首批 Linux ASR 演进路线（确定范围）

根据项目规划，首批 Linux ASR 转录适配将**同时覆盖 NVIDIA GPU 与纯 CPU 硬件**，具体方案包括：

1. **NVIDIA GPU 方案**：
   - 基于官方 Qwen3-ASR 或 Faster-Whisper GPU 运行时，利用 CUDA 核心实现高并发高速转录。
   - 适配标准 `/transcribe` 服务接口协议，与 Muse 无缝对接。
2. **纯 CPU 方案**：
   - 针对无独显的 Linux NAS / 软路由设备，集成 `faster-whisper` 多线程 CPU 推理。
   - 自动适配 CPU 核心数与轻量量化模型（如 `base` / `small`），保障低资源占用。

相关方案已完成前期技术调研（见 `docs/research/linux-transcription.md`），即将在后续任务中正式编码落地并交付实测。
