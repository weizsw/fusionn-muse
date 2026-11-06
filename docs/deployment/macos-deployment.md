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
# 返回: {"status":"healthy"}
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

当 Muse 完成音视频提取、转录与翻译后，会将无缝压制或配对的视频文件连同中文字幕输出至：
`/Volumes/External/automation/scraping`（容器内对应 `/data/automation/scraping`）。

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

## 六、分级冒烟测试与全链路验证

部署完成后，请按以下两个层级进行验证，切勿仅凭 Webhook 返回 202 就判定全链路部署成功：

### 级别 1：接口投递与 Webhook 连通性测试

此阶段仅验证 qBittorrent 脚本、网络连通性与 SQLite 入库：

```bash
# 执行回调脚本测试投递
python3 /Users/your-username/Development/scripts/torrent_complete.py \
  "SNOS-411" \
  "whisparr" \
  "mock-hash-001" \
  "/Volumes/External/torrents/whisparr/SNOS-411.mp4"
```

1. **终端输出**：应显示 `Accepted submission: <submission_id>`。
2. **查询 Submission 入库状态**：
   ```bash
   curl -s http://127.0.0.1:4444/api/v1/tasks/submission/<submission_id> | jq .
   ```
   确认返回包含正确的 `submission_id`。
   > **注意**：如果该路径下的物理文件并不存在，Submission 提交后后台准备阶段会将其判定为文件不存在而终止。**此测试仅证明 Webhook 接收通道通畅，不能证明 ASR 转录与 LLM 翻译正常。**

### 级别 2：端到端真实媒体全链路验证

为了彻底检验媒体扫描、宿主机 ASR、容器内 LLM 翻译与产物交付全流程，请使用真实媒体样本进行端到端测试：

1. **准备有效样本文件**：
   在下载目录放置一个带有标准番号的真实视频文件（例如 `/Volumes/External/torrents/whisparr/SNOS-411.mp4`）。
   > **重要提醒**：文件大小需大于 200MB，否则会被内置广告过滤器识别为 sample/广告片直接剔除。
2. **发起 Webhook 投递**：
   ```bash
   python3 /Users/your-username/Development/scripts/torrent_complete.py \
     "SNOS-411" \
     "whisparr" \
     "mock-hash-real" \
     "/Volumes/External/torrents/whisparr/SNOS-411.mp4"
   ```
3. **轮询观察任务流水线状态**：
   通过番号代码查询任务详情与阶段：
   ```bash
   curl -s http://127.0.0.1:4444/api/v1/tasks/job/SNOS-411 | jq '{status: .task.status, stage: .task.stage}'
   ```
   流水线将依次经历：`stage: "transcribing"`（宿主机 MLX ASR 转录）➔ `stage: "translating"`（PySubtrans LLM 翻译）➔ 最终状态变为 `status: "completed"`。
4. **验证物理交付产物**：
   检查输出目录 `/Volumes/External/automation/scraping/`：
   ```bash
   ls -la /Volumes/External/automation/scraping/
   ```
   **判定成功标准**：
   - 存在规范命名的视频文件（如 `SNOS-411.mp4`）。
   - 存在对应的同名中文字幕文件（如 `SNOS-411.zh-CN.srt` 或 `SNOS-411.srt`）。
   - 字幕文件内容非空，且包含时间戳和由 LLM 翻译的地道中文内容。
   只有完成上述检查，才证明整套转录与翻译流水线已完全具备生产交付能力！
