# AI Agent 一键部署使用说明书

本文档是专为 **AI 编码助手（如 Claude Code, Pi, Cursor, OpenCode, Codex, TDS Agent 等）** 设计的**自动化部署执行规范（Playbook）**。

当用户对 Agent 说：“**请帮我在本机部署 Fusionn-Muse**”时，Agent 应直接加载并按照本文档的标准化流程执行自动化部署、环境探测、配置生成与全链路连通性自检。

---

## 一、部署职责分工界面

Agent 在开始前必须清晰区分**自动化范围**与**用户交互范围**：

### 1. Agent 必须自动完成的步骤
1. 自动探测系统架构（macOS Apple Silicon / Linux x86_64 / arm64）、Docker 环境与 Python 运行时。
2. 自动在宿主机上创建规范的目录层级（`torrents`, `automation/*`, `strm/adults`）。
3. 自动生成生产就绪的 `config.yaml` 与 `docker-compose.yml`。
4. 在 macOS (Apple Silicon) 上自动配置宿主机 ASR Python 虚拟环境、安装依赖并注册 `launchd` plist 常驻守护进程。
5. 自动配置 `torrent_complete.py` 脚本参数与执行权限。
6. 自动启动 Docker 容器并执行端到端健康自检与 Webhook 模拟测试。

### 2. 必须由用户提供或由用户手动操作的步骤
1. **用户需提供的参数**：
   - 宿主机媒体存储根路径（例如 `/Volumes/External` 或 `/mnt/storage`）。
   - LLM 翻译凭据（API Key、提供商例如 OpenAI/DeepSeek/SiliconCloud 或自建 Custom 地址与模型名）。
   - 端口偏好（默认 4444 或 8080）。
2. **用户需在第三方 UI 界面手动完成的操作**：
   - qBittorrent Web UI 中的“下载完成运行外部程序”命令粘贴。
   - mdc-ng Web UI 中的账号初始化与监控路径确认。
   - 115strm 插件的网盘扫码登录。

---

## 二、Agent 标准化执行流程 (8 步工作流)

### Phase 1: 宿主机探测与交互参数确认

Agent 需首先在终端执行探测：

```bash
# 1. 检查操作系统与芯片架构
uname -s -m
# 2. 检查 Docker 与 Compose
docker --version && docker compose version
# 3. 检查 Python
python3 --version
```

> **系统支持边界检查（重要）**：
> 当前版本的一键全流程自动部署**仅支持搭载 Apple Silicon 芯片（M1/M2/M3/M4，`arm64` 架构）的 macOS 宿主机**。
> - 如果检测到宿主机为 **Linux** 或 Intel 芯片 macOS：Agent **必须明确向用户说明**：面向 Linux 平台（涵盖 NVIDIA GPU 与纯 CPU）的原生 ASR 语音转录适配正在专项开发推进中；当前 Linux 仅支持已有中文字幕视频的检测放行与流转，无法直接部署全功能转录。**切勿在 Linux 上跳过 ASR 配置后继续生成无效的 MLX ASR 指向并向用户误报完整部署成功**。
> - 如果检测到系统为 Apple Silicon macOS，则继续推进全自动部署流程。

**与用户交互确认**（若未预先提供）：
- 媒体存储根目录路径（`MEDIA_ROOT`，如 `/Volumes/External`）。
- 翻译服务类型（如 DeepSeek, OpenAI, SiliconCloud 等）及 `API_KEY`。

---

### Phase 2: 自动化创建目录体系

根据确认的 `MEDIA_ROOT`，Agent 运行：

```bash
MEDIA_ROOT="/Volumes/External" # 替换为用户实际值

mkdir -p "${MEDIA_ROOT}/torrents/whisparr"
mkdir -p "${MEDIA_ROOT}/automation/staging"
mkdir -p "${MEDIA_ROOT}/automation/processing"
mkdir -p "${MEDIA_ROOT}/automation/scraping"
mkdir -p "${MEDIA_ROOT}/automation/finished"
mkdir -p "${MEDIA_ROOT}/automation/subtitles"
mkdir -p "${MEDIA_ROOT}/automation/transcriptions"
mkdir -p "${MEDIA_ROOT}/automation/meta"
mkdir -p "${MEDIA_ROOT}/automation/failed"
mkdir -p "${MEDIA_ROOT}/strm/adults"
```

---

### Phase 3: 配置 macOS 宿主机 MLX ASR 转录服务（若系统为 macOS）

如果宿主机是搭载 Apple Silicon 的 macOS，执行：

```bash
DEPLOY_DIR="${HOME}/Development/fusionn-asr"
mkdir -p "${DEPLOY_DIR}/scripts"

# 1. 创建 Python 虚拟环境并安装依赖
python3 -m venv "${DEPLOY_DIR}/.venv"
"${DEPLOY_DIR}/.venv/bin/pip" install --upgrade pip
"${DEPLOY_DIR}/.venv/bin/pip" install fastapi uvicorn pydantic mlx-qwen3-asr

# 2. 复制服务端脚本与日志库
cp host_asr_server.py "${DEPLOY_DIR}/"
cp scripts/logfmt.py "${DEPLOY_DIR}/scripts/"

# 3. 编写 launchd plist 守护配置
PLIST_PATH="${HOME}/Library/LaunchAgents/com.fusionn.host-asr.plist"
cat <<EOF > "${PLIST_PATH}"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.fusionn.host-asr</string>
    <key>ProgramArguments</key>
    <array>
        <string>${DEPLOY_DIR}/.venv/bin/python3</string>
        <string>${DEPLOY_DIR}/host_asr_server.py</string>
    </array>
    <key>WorkingDirectory</key>
    <string>${DEPLOY_DIR}</string>
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
        <string>${DEPLOY_DIR}/.venv/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    </dict>
</dict>
</plist>
EOF

# 4. 加载并启动守护服务
launchctl unload "${PLIST_PATH}" 2>/dev/null || true
launchctl load "${PLIST_PATH}"
launchctl start com.fusionn.host-asr

# 5. 等待并验证 ASR 服务启动
sleep 2
curl -s http://127.0.0.1:8766/health
```

---

### Phase 4: 生成 `config.yaml` 配置文件

在部署目录（如 `${HOME}/Development/fusionn/configs/config.yaml`）渲染配置：

```yaml
server:
  port: 8080

pipeline:
  provider: "mlx_qwen3_asr"

mlx_qwen3_asr:
  server_url: "http://host.docker.internal:8766"
  host_prefix: "{{MEDIA_ROOT}}"
  container_prefix: "/data"
  model: "Qwen/Qwen3-ASR-1.7B"
  language: "ja"
  timeout_minutes: 180

translate:
  provider: "{{TRANSLATE_PROVIDER}}" # 如 openai, deepseek, siliconcloud, custom
  model: "{{TRANSLATE_MODEL}}"       # 如 gpt-4o-mini 或 deepseek-chat
  api_key: "{{TRANSLATE_API_KEY}}"
  target_lang: "Simplified Chinese"
  custom_server: "{{CUSTOM_SERVER_URL}}" # 仅当 provider 为 custom 时生效

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

hard_sub_ocr:
  enabled: true

queue:
  db_path: "/data/automation/meta/fusionn-muse.db"
  max_retries: 3
  retry_delay_ms: 10000
```

---

### Phase 5: 生成 `docker-compose.yml` 并启动容器

在工作目录下创建并启动：

```yaml
services:
  fusionn-muse:
    image: ghcr.io/weizsw/fusionn-muse:latest
    container_name: fusionn-muse
    restart: unless-stopped
    stop_grace_period: 45s
    ports:
      - "{{HOST_PORT}}:8080" # 推荐 4444:8080
    volumes:
      - ./configs/config.yaml:/app/config/config.yaml:ro
      - {{MEDIA_ROOT}}:/data
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

启动命令：
```bash
docker compose up -d
```

---

### Phase 6: 部署并配置 qBittorrent Webhook 脚本

1. 将 `scripts/torrent_complete.py` 部署到宿主机常用脚本目录（如 `${HOME}/Development/scripts/torrent_complete.py`）。
2. 修改脚本顶部的常量以匹配实际环境：
   - `MUSE_URL = os.environ.get("MUSE_URL", "http://127.0.0.1:{{HOST_PORT}}")`
   - `HOST_INPUT_PATH = "{{MEDIA_ROOT}}"`
   - `CONTAINER_INPUT_PATH = "/data"`
3. 执行 `chmod +x ${HOME}/Development/scripts/torrent_complete.py`。

---

### Phase 7: 使用共用命令验证投递与产物

从仓库根目录执行，使用与教程同版本的 `scripts/verify_deployment.py` 和 `scripts/torrent_complete.py`。完整参数、退出码及真实模型验收步骤见[部署检查命令](verification.md)。Agent 不另写回执解析、轮询或固定番号检查逻辑。

先只读检查：设置 `MUSE_URL` 为实际 Muse 地址（例如 `http://127.0.0.1:4444`）。

<!-- muse-verification-example -->
```bash
python3 scripts/verify_deployment.py health --url "${MUSE_URL:?请设置 Muse 地址}"
```

在用户授权的隔离部署中准备有效样本。设置 `MEDIA_ROOT` 为挂载到 `/data` 的独立目录，`SAMPLE_VIDEO` 为其中的真实视频，`SAMPLE_NAME` 为标准名称，`VERIFY_TIMEOUT` 为允许等待的秒数。以下命令会实际投递一次，并可能消耗模型资源：

<!-- muse-verification-example -->
```bash
python3 scripts/verify_deployment.py run \
  --url "${MUSE_URL:?}" --host-root "${MEDIA_ROOT:?}" --container-root /data \
  --sample "${SAMPLE_VIDEO:?}" --name "${SAMPLE_NAME:?}" --category whisparr \
  --expect subtitles --timeout "${VERIFY_TIMEOUT:?}"
```

Agent 读取 stdout 的 JSON：退出码 0 只代表所选目标通过；1 为已观察到失败；2 为证据不足或不适用。`accepted` 只证明入库，`delivered` 核对交付视频及记录的字幕，`subtitles` 额外要求非放行、非 dry-run 的源/目标字幕。已有中文字幕样本选择 `delivered`。

必须分别报告投递、产物和真实模型证据。`asr_invoked_this_run`、`llm_invoked_this_run` 在首版恒为 `not_proven`；不要把命令退出 0 写成“ASR/LLM 已实测”。实际模型调用、qBittorrent 自动触发及下游消费依照[独立验收步骤](verification.md#真实部署还要验证什么)留证。超时或回执丢失不自动重投、清库、删除字幕或恢复任务。

---

### Phase 8: 向用户输出结构化移交报告

Agent 完成上述所有步骤后，向用户汇报：

1. **服务运行状态**：
   - Fusionn-Muse API: `http://localhost:{{HOST_PORT}}`
   - ASR 服务: `http://localhost:8766`
2. **qBittorrent 设置指引**：
   - 打开 qBittorrent → 设置 → 下载 → 勾选“torrent 完成时运行外部程序”，填入：
     ```bash
     python3 /path/to/torrent_complete.py "%N" "%L" "%I" "%F"
     ```
3. **mdc-ng 路径指引**：
   - 监控目录：`/external/automation/scraping`（**务必开启“兼容监控模式”**）
   - 整理目录：`/external/automation/finished`
   - 元数据目录：`/external/strm/adults`
   - 字幕库目录：`/external/automation/subtitles`
4. **115strm 与 Emby 对应**：
   - 115strm 监控 `/Volumes/External/automation/finished`，上传后在 `/Volumes/External/strm/adults` 写入 `.strm`。
   - Emby 媒体库添加路径 `/Volumes/External/strm/adults`。
