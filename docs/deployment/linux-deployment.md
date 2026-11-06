# Linux 部署与架构说明

本文档说明 **Fusionn-Muse** 在 Linux 操作系统下的当前部署方式、运行状态及后续转录方案适配规划。

---

## 一、平台支持现状客观说明

| 模块 / 环节 | Linux 运行状态 | 说明 |
|------------|--------------|------|
| **Muse 核心服务** (Go + 队列调度) |  完全原生支持 | 标准 Docker 容器化运行，支持 x86_64 与 arm64 |
| **媒体扫描与过滤** (广告过滤/番号提取/ISO合并) |  完全原生支持 | 依赖容器内内置的 ffmpeg 与 ffprobe，无平台依赖 |
| **中文字幕检测** (文件名/外挂/内嵌) |  完全原生支持 | 纯文本与 ffprobe 流元数据检测 |
| **硬字幕 OCR 检出** (RapidOCR) |  完全原生支持 | 基于 CPU ONNX Runtime，容器内独立执行，支持 Linux |
| **LLM 字幕翻译** (PySubtrans Custom Server) |  完全原生支持 | 容器内直接发起 HTTP 翻译请求至外部 OpenAI 兼容端点 |
| **下游联动** (mdc-ng / 115strm / Emby) |  完全原生支持 | 纯标准目录监控与文件流转 |
| **本地语音转录 (ASR)** | ⚠️ **专项调研适配中** | 当前生产实测方案基于 macOS Apple Silicon 的 MLX 框架。Linux 平台无法运行 MLX，适配工作正在独立调查推进中。 |

> **Linux 语音转录现状说明**：
> macOS 上的超低延迟与低资源消耗得益于 Apple Silicon 的统一内存架构与 MLX 框架。对于 Linux 平台，官方正在进行独立的 ASR 适配调查，评估包括容器化/宿主机部署的 **Faster-Whisper**、**Whisper.cpp**、**FunASR** 或 **vLLM / Qwen-ASR** 等方案。在官方 Linux ASR 模块正式发布之前，Linux 用户可参考下文的部署方案。

---

## 二、当前 Linux 用户的部署与使用方式

在 Linux 环境下，用户可以非常顺畅地使用 Muse 处理**硬字幕检测过滤**、**已有字幕翻译**以及**多媒体自动化流转**。

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

在终端快速创建目录结构：

```bash
STORAGE_ROOT="/mnt/storage"  # 替换为你的实际路径

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
    extra_hosts:
      - "host.docker.internal:host-gateway"
    logging:
      driver: json-file
      options:
        max-size: "20m"
        max-file: "6"
```

### 3. 配置 `config.yaml`

如果视频已有内嵌/外挂字幕或硬编码字幕，Muse 会自动通过 OCR 检出并直接送入翻译或刮削交付，无需调用本地 ASR：

```yaml
server:
  port: 8080

pipeline:
  provider: "mlx_qwen3_asr"

# 若 Linux 宿主机架设了兼容的转录服务代理（返回 SRT 路径），可指向宿主机网关
mlx_qwen3_asr:
  server_url: "http://host.docker.internal:8766"
  host_prefix: "/mnt/storage"
  container_prefix: "/data"
  model: "Qwen/Qwen3-ASR-1.7B"
  language: "ja"
  timeout_minutes: 180

translate:
  provider: "openai"              # 或 "custom" / "deepseek"
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

# 开启硬字幕检测（关键：自动识别已有中文字幕，避免无谓转录）
hard_sub_ocr:
  enabled: true

queue:
  db_path: "/data/automation/meta/fusionn-muse.db"
  max_retries: 3
  retry_delay_ms: 10000
```

### 4. 启动服务与检查

```bash
docker compose up -d

# 检查健康状态
curl -s http://127.0.0.1:4444/api/v1/health
```

---

## 三、后续 Linux ASR 演进规划

对于无字幕生肉视频的本地转录，团队正在评估以下 Linux 适配路线：

1. **容器化 Whisper 方案**：
   - 使用 `faster-whisper` 或 `whisper.cpp` 封装为轻量级独立 Sidecar 容器。
   - 支持 NVIDIA CUDA 硬件加速以及纯 CPU 多线程转录。
2. **国产模型方案 (FunASR / Qwen ASR GPU)**：
   - 适配对中日双语及成人专有名词理解力更强的国产开源语音模型。
   - 提供标准 Dockerfile 与 docker-compose 模板，支持一键拉起。

适配完成后，官方将更新发布支持 Linux 本地 GPU/CPU 转录的一键配置方案与预构建镜像。
