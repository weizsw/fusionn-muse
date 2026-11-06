# Fusionn-Muse

专为 **qBittorrent** 与家庭媒体中心（Emby / Jellyfin / Plex）设计的全自动音视频字幕转录、LLM 批量翻译与影视库联动处理流水线。

当 qBittorrent 下载完成时，Fusionn-Muse 接收 Webhook 触发，在后台可靠地完成媒体分析过滤、语音转录、LLM 地道中文字幕翻译，并交付至 `scraping` 监控目录，与 **mdc-ng** 刮削器、**115strm** 网盘软链接工具及 **Emby** 媒体服务器实现端到端自动化联动。

---

## 核心特性

- 🎤 **Apple Silicon MLX 宿主机硬件加速**：在搭载 M 系列芯片的 macOS 宿主机上原生运行 MLX Qwen3 ASR，直接调用 Metal GPU 与统一内存加速，不占 Docker 资源，无需在容器内下载巨大模型，转录极速且显存彻底释放。
- 🌐 **PySubtrans + LLM 批量智能翻译**：原生支持 OpenAI 兼容 API（OpenAI, DeepSeek, SiliconCloud, Groq, OpenRouter 及自建端点），专为长文本优化分批重试机制；支持针对日本成人影片等专业领域的日中俚语、现代口语与专有名词 Prompt 指导词。
- 📥 **qBittorrent Webhook 全自动触发**：通过轻量级无第三方依赖的 Python 脚本，仅针对指定分类（如 `whisparr`）自动发起请求，秒级响应并进入后台任务队列。
- 🔍 **智能媒体与中文字幕检出**：自动剔除广告预告与样本文件（≤200MB）、精准提取番号与规范化媒体代码、支持多段分卷视频合并与 ISO 光盘镜像免挂载提取；智能识别文件名特征、同名外挂字幕、内嵌字幕轨，并内置 RapidOCR CPU 硬字幕检测，杜绝无谓重复转录。
- 🎬 **下游 mdc-ng + 115strm + Emby 全流程无缝联动**：Muse 交付产物至 `scraping` 目录触发 mdc-ng 自动刮削元数据并整理；115strm 监听并上传原画至网盘，在 Emby 本地目录生成 `.strm` 流媒体软链接，实现“**本地刮削 + 本地中文字幕 + 云端原画秒开**”的终极观影体验！
- 🛡️ **高可靠 SQLite 任务状态持久化**：基于 SQLite WAL 机制，断电或重启不丢任务；支持灵活的任务查询（支持番号代码直查如 `SNOS-411`）、单任务重试/恢复/二次翻译，以及批量恢复操作。
- 🪵 **Logfmt 统一结构化日志**：严格遵循结构化单行日志规范，自动脱敏 API Key 与敏感凭据，支持动态日志级别与时区对齐。

---

## 端到端自动化架构全景

```text
┌─────────────────┐
│  qBittorrent    │ 下载完成
└────────┬────────┘
         │ 调用外部脚本: python3 torrent_complete.py "%N" "%L" "%I" "%F"
         ▼
┌────────────────────────────────────────────────────────────────────────┐
│  Fusionn-Muse 核心流水线 (Docker 容器)                                  │
│                                                                        │
│  1. 接收 Webhook ──► SQLite WAL 持久化入库 (返回 HTTP 202)              │
│  2. 媒体准备与分析 ──► 过滤广告样本 / 识别番号代码 / 多段合并 / ISO 提取   │
│  3. 字幕智能检测 ──► 文件名/外挂/内嵌字幕检测 + RapidOCR 硬字幕检测     │
│  4. 语音转录 (ASR) ──► 请求 macOS 宿主机 MLX Qwen3 ASR 服务 (Metal GPU) │
│  5. LLM 字幕翻译 ──► PySubtrans 调用 OpenAI 兼容端点 (定制化日中口语Prompt)│
│  6. 产物交付输出 ──► 输出视频与中文字幕至 scraping 目录                 │
└────────┬───────────────────────────────────────────────────────────────┘
         │
         ▼ 交付至 /Volumes/External/automation/scraping
┌─────────────────┐
│     mdc-ng      │ 监控 scraping 目录 (采用“兼容监控模式”/轮询，避免跨卷漏检)
└────────┬────────┘
         ├─ 自动抓取海报、剧照、NFO 元数据 ──► 写入本地 Emby 媒体库目录 (/strm/adults/)
         ├─ 提取整理中文字幕 ────────────────► 归档至中文字幕库 (/automation/subtitles/)
         └─ 整理重命名视频文件 ──────────────► 移动至已完成目录 (/automation/finished/)
                                                      │
                                                      ▼ 监控 finished 目录
                                            ┌─────────────────┐
                                            │    115strm      │
                                            └────────┬────────┘
                                                     ├─ 自动将大视频上传至 115 网盘
                                                     ├─ 在 Emby 库 (/strm/adults/) 生成对应 .strm 文件
                                                     └─ 删除本地大视频文件 (释放本地磁盘存储)
                                                              │
                                                              ▼
                                                    ┌─────────────────┐
                                                    │  Emby / 播放器  │
                                                    └─────────────────┘
                                                    读取本地海报/NFO与本地中文字幕，
                                                    通过 .strm 秒级原画播放网盘视频！
```

---

## 跨服务存储与路径拓扑对照

跨容器部署时，服务间通过宿主机目录进行文件流转。推荐将宿主机媒体存储根目录（例如 `/Volumes/External`）统一映射至各容器：

| 业务环节 | 宿主机物理路径示例 | Muse 容器内映射 | mdc-ng 容器内映射 | Emby 容器内映射 | 职责与作用 |
|:---|:---|:---|:---|:---|:---|
| **媒体存储根目录** | `/Volumes/External` | `/data` | `/external` | `/data` | 统一数据卷挂载点 |
| **qB 下载目录** | `/Volumes/External/torrents/whisparr` | `/data/torrents/whisparr` | - | - | 原始下载输入源（只读） |
| **Muse 待处理区** | `/Volumes/External/automation/staging` | `/data/automation/staging` | - | - | Muse 内部队列暂存 |
| **Muse 工作区** | `/Volumes/External/automation/processing` | `/data/automation/processing`| - | - | Muse 当前正在处理的工作区 |
| **Muse 交付输出区** | `/Volumes/External/automation/scraping` | `/data/automation/scraping` | `/external/automation/scraping` | - | **Muse 输出 / mdc-ng 监控输入** |
| **mdc-ng 整理归档区** | `/Volumes/External/automation/finished` | `/data/automation/finished` | `/external/automation/finished` | - | **mdc-ng 整理输出 / 115strm 监听上传** |
| **中文字幕库** | `/Volumes/External/automation/subtitles` | `/data/automation/subtitles` | `/external/automation/subtitles` | - | 中文字幕集中归档库 |
| **原始转录存档** | `/Volumes/External/automation/transcriptions` | `/data/automation/transcriptions` | - | - | 原始日文字幕存档（供二次翻译） |
| **SQLite 数据库** | `/Volumes/External/automation/meta` | `/data/automation/meta` | - | - | 持久化任务与尝试状态数据库 |
| **Emby 媒体库目录** | `/Volumes/External/strm/adults` | - | `/external/strm/adults` | `/data/adults` | **mdc-ng 输出元数据 / 115strm 输出 .strm** |

---

## 平台支持与架构方案 (macOS vs Linux)

### 1. macOS (Apple Silicon M1/M2/M3/M4) —— 【实测推荐方案】
- **运行拓扑**：Docker 运行 Muse 容器 + macOS 宿主机原生运行 `host_asr_server.py`。
- **核心优势**：利用 macOS 原生 Apple MLX 机器学习框架，直接使用 M 系列芯片的统一内存与 Metal GPU 进行语音转录。单部电影（2小时）转录仅需几分钟，显存随转录结束立即释放，不占用 Docker 虚拟机内存。
- **详尽步骤**：参见 [macOS 部署教程](docs/deployment/macos-deployment.md)。

### 2. Linux —— 【架构说明与演进规划】
- **当前运行状态**：Muse 核心容器、任务队列、媒体准备、中文字幕检测、RapidOCR（CPU ONNX）硬字幕检测与 LLM 翻译均**完全支持 Linux 原生运行**。
- **语音转录说明**：因 Linux 无法运行 Apple MLX 框架，针对 Linux（涵盖 NVIDIA GPU 与纯 CPU 硬件）的 ASR 转录适配正在专项开发推进中。当前版本下，Muse 在 Linux 上支持媒体准备过滤与中文字幕智能检出（对于已包含中文字幕的视频直接放行交付至 `scraping` 目录供下游刮削）；对于缺少中文字幕需进行语音转录的视频，当前版本尚无法在 Linux 本地完成 ASR 转录（推荐在 macOS Apple Silicon 上部署全功能版本）。
- **详尽说明**：参见 [Linux 部署与架构说明](docs/deployment/linux-deployment.md)。

---

## 快速开始

### 步骤一：创建宿主机目录

```bash
MEDIA_ROOT="/Volumes/External" # 替换为你的实际物理存储根路径

mkdir -p "${MEDIA_ROOT}/torrents/whisparr"
mkdir -p "${MEDIA_ROOT}/automation/"{staging,processing,scraping,finished,subtitles,transcriptions,meta,failed}
mkdir -p "${MEDIA_ROOT}/strm/adults"
```

### 步骤二：编写 `docker-compose.yaml`

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
      - ./config/config.yaml:/app/config/config.yaml:ro
      - /Volumes/External:/data
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

### 步骤三：准备配置文件 `config/config.yaml`

复制示例配置并修改：

```bash
cp config/config.example.yaml config/config.yaml
```

关键配置示例：

```yaml
server:
  port: 8080

pipeline:
  provider: "mlx_qwen3_asr"

# 宿主机 ASR 转录服务配置
mlx_qwen3_asr:
  server_url: "http://host.docker.internal:8766"
  host_prefix: "/Volumes/External"
  container_prefix: "/data"
  model: "Qwen/Qwen3-ASR-1.7B"
  language: "ja"
  timeout_minutes: 180

# 翻译服务设置（支持 openai, deepseek, siliconcloud, groq, custom 等）
translate:
  provider: "openai"              # 或 "custom" / "deepseek"
  model: "gpt-4o-mini"
  api_key: "sk-your-api-key"
  target_lang: "Simplified Chinese"
  # custom_server: "http://your-server-ip:port/v1" # 仅当 provider 为 custom 时填写

  # 示例：针对日本成人影片定制的地道翻译指导词
  instruction: |
    你是一个经验丰富的日中翻译专家，专门处理日本成人影片的字幕翻译。精通日语俚语、成人行业术语与现代中文自然表达。
    保持忠实原意，语言口语化，成人常见用语（おっぱい→奶子/胸部，中出し→内射，マンコ→小穴/骚逼，チンポ→鸡巴/大屌等）准确传达，保留时间戳与字幕结构。

llm_subtrans:
  timeout_minutes: 180
  rate_limit: 0
  max_retries: 10
  request_timeout_seconds: 300
  retry_backoff_seconds: 0
  min_batch_size: 25
  max_batch_size: 60

# 启用 CPU OCR 检测压制硬字幕（检出后直接跳过无谓转录）
hard_sub_ocr:
  enabled: true

queue:
  db_path: "/data/automation/meta/fusionn-muse.db"
  max_retries: 3
  retry_delay_ms: 10000
```

### 步骤四：在 macOS 宿主机启动 MLX ASR 服务

在宿主机终端中准备 Python 虚拟环境并运行：

```bash
python3 -m venv ~/Development/mlx-asr-venv
source ~/Development/mlx-asr-venv/bin/activate
pip install fastapi uvicorn pydantic mlx-qwen3-asr

# 启动宿主机转录服务端（监听 8766 端口）
python3 host_asr_server.py
```
> 如需开机后台自启，请参见 [macOS 部署教程中的 launchd 配置](docs/deployment/macos-deployment.md)。

### 步骤五：启动 Muse 核心容器

```bash
docker compose up -d
```

### 步骤六：配置 qBittorrent 完成调用

1. 将 `scripts/torrent_complete.py` 保存至宿主机（例如 `/Users/username/Development/scripts/torrent_complete.py`）。
2. 打开 qBittorrent：**设置 → 下载 → 勾选“torrent 完成时运行外部程序”**，填入：
   ```bash
   python3 /Users/username/Development/scripts/torrent_complete.py "%N" "%L" "%I" "%F"
   ```
3. 该脚本仅对分类为 `whisparr` 的任务发起 HTTP 202 请求，其他分类自动跳过，保障下载客户端稳定性。

### 步骤七：配置下游 mdc-ng 刮削服务

在 mdc-ng Web 管理页面（`http://<IP>:9208`）中配置关键路径：
- **监控目录**：`/external/automation/scraping`
- **监控模式**：**必须选择“兼容监控模式” (watcher_kind: 1)**（通过轮询确保 Docker 卷文件移动不会漏掉）
- **整理目录**：`/external/automation/finished`
- **元数据目录**：`/external/strm/adults`（此目录直接作为本地 Emby 媒体库目录）
- **字幕库目录**：`/external/automation/subtitles`

详细说明参见 [mdc-ng 刮削与 115strm 联动指南](docs/deployment/mdc-ng-integration.md)。

---

## AI Agent 一键部署说明

如果你正在使用 AI 编程助手（如 **Claude Code**, **Pi**, **Cursor**, **OpenCode**, **TDS Agent** 等），可以直接将以下指引交给 Agent，由 Agent 全自动完成环境检测、目录创建、服务搭建与全链路冒烟自测：

👉 **[AI Agent 一键部署使用说明书 (Playbook)](docs/deployment/agent-deployment.md)**

该文档包含 Agent 标准化执行的 8 步全自动化规程以及必须由用户提供的凭据清单，能够实现真正意义上的“一键全自动部署”。

---

## 完整部署教程列表

- 📖 **[macOS (Apple Silicon) 部署指南](docs/deployment/macos-deployment.md)**：包含 MLX ASR 搭建、launchd 常驻服务、Docker Compose 及联调步骤。
- 📖 **[Linux 部署与架构说明](docs/deployment/linux-deployment.md)**：包含 Linux 原生运行支持、硬字幕检测工作流及 Linux ASR 后续演进规划。
- 📖 **[mdc-ng 刮削、115strm 与 Emby 联动指南](docs/deployment/mdc-ng-integration.md)**：包含路径映射矩阵、mdc-ng 兼容监控模式配置、115strm 生成 strm 与 Emby 零本地空间占用串流方案。
- 🤖 **[AI Agent 一键部署使用说明书](docs/deployment/agent-deployment.md)**：专为 AI Agent 编写的标准化全自动部署工作手册。

---

## API 接口与运维管理

Fusionn-Muse 提供了强大的任务生命周期管理 API，支持通过番号代码（如 `SNOS-411`）或全局任务 ID 直接定位和操作任务：

| 请求方法 | 端点路径 | 功能描述 |
|:---|:---|:---|
| `POST` | `/api/v1/webhook/torrent` | qBittorrent 完成事件接收（立即持久化并返回 HTTP 202） |
| `GET` | `/api/v1/tasks` | 分页查询所有任务列表（支持 `status` 与 `kind` 过滤） |
| `GET` | `/api/v1/tasks/stats` | 获取当前队列与任务状态统计（pending, processing, failed 等） |
| `GET` | `/api/v1/tasks/:kind/:id` | 查询任务详情（`:id` 可直接使用番号代码如 `SNOS-411`） |
| `POST`| `/api/v1/tasks/:kind/:id/actions`| 单任务显式恢复动作（支持 `retry`, `resume`, `retranslate`） |
| `POST`| `/api/v1/tasks/actions` | 批量恢复动作（一键重试全部失败任务或恢复中断任务） |
| `GET` | `/api/v1/health` | 服务健康检查 |
| `GET` | `/api/v1/version` | 获取版本与构建信息 |

### 常用运维操作命令

```bash
# 1. 检查服务健康状态
curl -s http://localhost:4444/api/v1/health

# 2. 查询当前正在处理与失败的任务
curl -s 'http://localhost:4444/api/v1/tasks?status=processing' | jq .
curl -s 'http://localhost:4444/api/v1/tasks?status=failed' | jq .

# 3. 通过番号代码查询任务详情与可用动作
curl -s 'http://localhost:4444/api/v1/tasks/job/SNOS-411' | jq .

# 4. 对指定任务重新翻译（利用已持久化的高质量原始转录字幕）
curl -X POST 'http://localhost:4444/api/v1/tasks/job/SNOS-411/actions' \
  -H 'Content-Type: application/json' \
  -d '{"action":"retranslate"}'

# 5. 一键使用当前最新配置重试全部失败任务
curl -X POST 'http://localhost:4444/api/v1/tasks/actions' \
  -H 'Content-Type: application/json' \
  -d '{"action":"retry","use_current_settings":true}'
```

---

## 本地开发与构建

### 环境要求
- Go 1.23+
- Python 3.10+
- Docker

### 命令速查
```bash
# 编译二进制程序
make build

# 运行代码规范检查
make lint

# 运行单元测试
make test

# 构建 Docker 镜像
make docker
```

---

## 开源协议

本项目采用 [GNU General Public License v3.0 (GPL-3.0)](LICENSE) 许可证开源。
