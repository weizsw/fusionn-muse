# macOS (Apple Silicon) 部署指南

本指南详细介绍如何在搭载 Apple Silicon 芯片（M1/M2/M3/M4）的 macOS 宿主机上完整部署 **Fusionn-Muse** 媒体字幕自动化流水线。

该部署方案为当前**官方实测与推荐方案**：
- **核心服务（Fusionn-Muse）**：运行在 Docker 容器中，负责任务队列、调度、媒体扫描过滤与 LLM 翻译。
- **语音转录（MLX Qwen3 ASR）**：原生运行在 macOS 宿主机上，通过 Apple MLX 框架调用 Metal GPU 和统一内存加速，无需在容器内下载庞大模型，转录极快且显存释放彻底。
- **触发与交付链路**：qBittorrent 完成下载后触发 Webhook，Muse 处理完成后输出至 `scraping` 监控目录，下游 mdc-ng 刮削元数据并整理，115strm 生成流媒体文件至本地 Emby 库。

---

## 一、架构与目录规划

为了让 Docker 容器与 macOS 宿主机以及下游服务共享文件且避免跨盘复制开销，推荐采用**统一媒体根目录**映射方案。

假设宿主机存储根路径为 `/Volumes/External`（例如外置硬盘或指定工作目录）：

```text
/Volumes/External/
├── torrents/                  # qBittorrent 默认下载目录（输入源）
│   └── whisparr/              # 分类下载目录（仅处理该分类下的内容）
│       └── ABC-123.mp4
│
├── automation/                # Muse 与自动化工作目录
│   ├── staging/               # [Muse] 队列排队暂存
│   ├── processing/            # [Muse] 正在处理中工作目录
│   ├── scraping/              # [Muse输出/mdc-ng输入] 待刮削目录
│   ├── finished/              # [mdc-ng整理输出] 视频归档（115strm监听此目录上传）
│   ├── subtitles/             # [Muse输出/mdc-ng输入] 中文字幕库
│   ├── transcriptions/        # [Muse] 原始日文转录字幕存档（供二次翻译）
│   ├── meta/                  # [Muse] fusionn-muse.db 持久化 SQLite 数据库
│   └── failed/                # [Muse] 失败任务记录与故障排查
│
└── strm/
    └── adults/                # 本地 Emby 媒体库目录（mdc-ng 存放海报/NFO，115strm 存放 .strm）
```

在终端中一键创建所需目录：

```bash
MEDIA_ROOT="/Volumes/External"  # 请替换为你的实际存储路径

mkdir -p "${MEDIA_ROOT}/torrents/whisparr"
mkdir -p "${MEDIA_ROOT}/automation/"{staging,processing,scraping,finished,subtitles,transcriptions,meta,failed}
mkdir -p "${MEDIA_ROOT}/strm/adults"
```

---

## 二、部署宿主机 MLX Qwen3 ASR 语音转录服务

### 1. 准备 Python 虚拟环境与依赖

macOS 推荐使用 Python 3.10 或 3.11 环境：

```bash
# 进入独立开发或脚本目录，例如 ~/Development/mlx-qwen3-asr
mkdir -p ~/Development/mlx-qwen3-asr
cd ~/Development/mlx-qwen3-asr

# 创建 Python 虚拟环境
python3 -m venv .venv
source .venv/bin/activate

# 安装基础 Web 依赖与 mlx-qwen3-asr
pip install --upgrade pip
pip install fastapi uvicorn pydantic mlx-qwen3-asr
```

### 2. 部署服务脚本

将仓库根目录下的 `host_asr_server.py` 与 `scripts/logfmt.py` 复制或软链接至该目录：

```bash
# 假设 fusionn-muse 仓库克隆在 ~/Development/fusionn-muse
cp ~/Development/fusionn-muse/host_asr_server.py ~/Development/mlx-qwen3-asr/
mkdir -p ~/Development/mlx-qwen3-asr/scripts
cp ~/Development/fusionn-muse/scripts/logfmt.py ~/Development/mlx-qwen3-asr/scripts/
```

### 3. 测试启动宿主机服务

```bash
source .venv/bin/activate
python3 host_asr_server.py
```

终端将显示服务已启动于 `http://0.0.0.0:8766`。打开另一个终端测试连通性：

```bash
curl -s http://127.0.0.1:8766/health
# 返回: {"status":"healthy","service":"host-asr"}
```

### 4. 设置为 macOS 后台常驻服务（launchd）

为了让转录服务在系统开机或用户登录时自动在后台启动，推荐使用 `launchd` 配置 plist：

创建 `~/Library/LaunchAgents/com.fusionn.host-asr.plist`：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.fusionn.host-asr</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/your-username/Development/mlx-qwen3-asr/.venv/bin/python3</string>
        <string>/Users/your-username/Development/mlx-qwen3-asr/host_asr_server.py</string>
    </array>
    <key>WorkingDirectory</key>
    <string>/Users/your-username/Development/mlx-qwen3-asr</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/host-asr.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/host-asr.err</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/Users/your-username/Development/mlx-qwen3-asr/.venv/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    </dict>
</dict>
</plist>
```

> **注意**：请将上述 `/Users/your-username` 替换为你的实际 macOS 用户目录。

加载并启动服务：

```bash
launchctl load ~/Library/LaunchAgents/com.fusionn.host-asr.plist
launchctl start com.fusionn.host-asr
```

---

## 三、部署 Fusionn-Muse 容器

### 1. 准备工作目录

```bash
mkdir -p ~/Development/fusionn/configs
cd ~/Development/fusionn
```

### 2. 编写 `docker-compose.yml`

创建 `docker-compose.yml`：

```yaml
services:
  fusionn-muse:
    image: ghcr.io/weizsw/fusionn-muse:latest
    container_name: fusionn-muse
    restart: unless-stopped
    stop_grace_period: 45s
    ports:
      # 宿主机端口:容器端口（可根据需要自定义宿主机端口，例如 4444:8080）
      - "4444:8080"
    volumes:
      - ./configs/config.yaml:/app/config/config.yaml:ro
      # 将宿主机媒体根目录挂载至容器 /data
      - /Volumes/External:/data
    environment:
      - ENV=production
      - TZ=Asia/Shanghai
      - PUID=501
      - PGID=20
      - LOG_LEVEL=debug
    extra_hosts:
      # 确保容器可以通过 host.docker.internal 请求宿主机 8766 端口的 ASR 服务
      - "host.docker.internal:host-gateway"
    logging:
      driver: json-file
      options:
        max-size: "20m"
        max-file: "6"
```

### 3. 配置 `configs/config.yaml`

创建 `configs/config.yaml`，填入你的 LLM 凭证与路径映射：

```yaml
server:
  port: 8080

pipeline:
  provider: "mlx_qwen3_asr"

# 宿主机 ASR 转录配置
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
  model: "gpt-4o-mini"            # 或你的自定义模型名
  api_key: "sk-your-api-key"
  target_lang: "Simplified Chinese"
  # custom_server: "http://your-server-ip:port/v1"  # 当 provider 为 custom 时填写

  # 针对日本成人影片等专业领域的翻译指导词（可选，按需使用）
  instruction: |
    你是一个经验丰富的日中翻译专家，专门处理日本成人影片的字幕翻译。精通日语俚语、成人行业术语与现代中文自然表达。
    保持忠实原意，语言口语化，成人常见用语（おっぱい→奶子/胸部，中出し→内射，マンコ→小穴/骚逼，チンポ→鸡巴/大屌等）准确传达，保留时间戳与字幕结构。

# LLM 翻译引擎参数微调（已针对长文本与重试进行最佳调优）
llm_subtrans:
  timeout_minutes: 180
  rate_limit: 0
  max_retries: 10
  request_timeout_seconds: 300
  retry_backoff_seconds: 0
  min_batch_size: 25
  max_batch_size: 60

# 开启硬字幕检测（过滤已压制中文字幕的视频，避免重复转录）
hard_sub_ocr:
  enabled: true

# 队列与重试
queue:
  db_path: "/data/automation/meta/fusionn-muse.db"
  max_retries: 3
  retry_delay_ms: 10000
```

### 4. 启动容器

```bash
docker compose up -d
```

验证 Muse 服务运行状态：

```bash
curl -s http://127.0.0.1:4444/api/v1/health
# 返回: {"status":"ok"}
```

---

## 四、配置 qBittorrent 下载完成调用

1. 将 `scripts/torrent_complete.py` 保存至宿主机脚本路径（例如 `/Users/your-username/Development/scripts/torrent_complete.py`）。
2. 确保脚本首行的路径映射与 Muse 端口一致：
   - `MUSE_URL = os.environ.get("MUSE_URL", "http://127.0.0.1:4444")`
   - `HOST_INPUT_PATH = "/Volumes/External"`
   - `CONTAINER_INPUT_PATH = "/data"`
3. 打开 qBittorrent：
   - 进入 **设置 (Preferences) → 下载 (Downloads)**
   - 勾选 **“torrent 完成时运行外部程序” (Run external program on torrent completion)**
   - 填入调用命令：
     ```bash
     python3 /Users/your-username/Development/scripts/torrent_complete.py "%N" "%L" "%I" "%F"
     ```
   - 参数说明：
     - `%N`：Torrent 标题
     - `%L`：分类名（脚本仅对分类为 `whisparr` 的任务发起请求，其他分类安全跳过）
     - `%I`：Info Hash
     - `%F`：下载内容在宿主机上的完整路径（如 `/Volumes/External/torrents/whisparr/ABC-123.mp4`）

---

## 五、下游 mdc-ng 与 115strm 联动配置

Muse 将视频交付到 `/Volumes/External/automation/scraping`（容器内 `/data/automation/scraping`）；最终外挂字幕位于 `/Volumes/External/automation/subtitles`。已有中文字幕的视频可以直接放行；这不代表本次执行了 ASR 或翻译。

### 1. 部署 mdc-ng 刮削服务

在 Docker 中启动 mdc-ng：

```yaml
services:
  mdcng:
    image: mdcng/mdc:latest
    container_name: mdcng
    restart: unless-stopped
    environment:
      - PUID=501
      - PGID=20
      - TZ=Asia/Shanghai
    volumes:
      - ./data:/config
      # 将宿主机媒体根目录挂载至 /external
      - /Volumes/External:/external
    ports:
      - "9208:9208"
```

### 2. 在 mdc-ng Web 管理页面配置路径

打开 `http://<IP>:9208`，在路径与监控设置中配置：
- **监控目录 (Watch Folder)**：`/external/automation/scraping`
- **监控模式**：选择 **“兼容监控模式” (Polling/watcher_kind: 1)**（确保 Docker 卷事件不会丢失）。
- **整理目录 (Target Folder)**：`/external/automation/finished`（视频处理后移动到此处）。
- **元数据目录 (Metadata Folder)**：`/external/strm/adults`（NFO、海报、剧照等元数据写入此处，即 Emby 媒体库目录）。
- **字幕库目录 (Subtitles Folder)**：`/external/automation/subtitles`。

### 3. 115strm 与 Emby 协同

1. **115strm 上传并生成软链接文件**：
   - 115strm 插件监控 `/Volumes/External/automation/finished`。
   - 自动上传视频至 115 网盘对应路径。
   - 上传完成后，在 Emby 媒体库目录 `/Volumes/External/strm/adults` 下生成与视频对应的同名 `.strm` 文件，并可自动删除本地 bulky 视频，释放巨额磁盘空间！
2. **Emby 本地极速播放**：
   - Emby 媒体库直接添加 `/Volumes/External/strm/adults`。
   - Emby 读取的是本地极速刮削的元数据、高清海报以及 Muse 生成的高质量中文字幕；视频流则直接通过 `.strm` 从网盘云端原画秒开！

---

## 六、使用共用命令验证部署

从仓库根目录运行。教程、agent 和测试使用同一个 `scripts/verify_deployment.py`；回调脚本也应使用同一版本。参数、结果解释与恢复观察方法见[部署检查命令](verification.md)。

先设置 `MUSE_URL`，例如 `http://127.0.0.1:4444`，进行只读检查：

<!-- muse-verification-example -->
```bash
python3 scripts/verify_deployment.py health --url "${MUSE_URL:?请设置 Muse 地址}"
```

如需同时检查宿主机 ASR 的 HTTP 连通性，可增加 `--asr-url http://127.0.0.1:8766`。这不证明容器可以访问它，也不证明模型能推理。

样本验证应在独立部署中运行，使用新数据库和独立的媒体、字幕及缓存目录，避免旧任务或下游搬运干扰；不要清理现网来制造测试环境。准备满足媒体过滤规则、具有音轨且无中文字幕的真实视频。设置 `MEDIA_ROOT` 为独立部署挂载到 `/data` 的目录，`SAMPLE_VIDEO` 为其中的视频绝对路径，`SAMPLE_NAME` 为标准名称，`VERIFY_TIMEOUT` 为允许等待的秒数（例如 14400）。

以下命令会投递一次样本并核对字幕产物：

<!-- muse-verification-example -->
```bash
python3 scripts/verify_deployment.py run \
  --url "${MUSE_URL:?}" --host-root "${MEDIA_ROOT:?}" --container-root /data \
  --sample "${SAMPLE_VIDEO:?}" --name "${SAMPLE_NAME:?}" --category whisparr \
  --expect subtitles --timeout "${VERIFY_TIMEOUT:?}"
```

已有中文字幕样本改用 `--expect delivered`；只核对入库使用 `--expect accepted`。命令退出 0 仅证明所选目标，JSON 中真实 ASR/LLM 调用仍为 `not_proven`。退出 1 表示已观察到目标失败，退出 2 表示证据不足或样本不适用。不能把旧文件存在、任务 completed 或字幕产物通过写成模型已实测。

保存命令返回的 Submission/Job/Attempt ID，按[真实部署验收步骤](verification.md#真实部署还要验证什么)补充模型执行与内容抽样证据。没有完成这些步骤前，报告应保留“真实模型未验证”。
