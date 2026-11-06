# Muse 开源部署方案：流程梳理、文档缺口与 Linux 转写选型

> 本文是方案，不是实施。没有修改运行代码。结论分三级标注：**已核实**（读代码或本机实测）、**有文档依据**（上游 README/源码，附出处）、**待实机验证**（需要真实 Linux CPU / NVIDIA 机器或用户的实际配置）。
>
> 依据：`main` 分支（ffdb2ba）。本次在仓库上运行 `make lint`（0 issues）与 `make test`（Go 与 Python 测试全部通过），确认梳理对象是一份健康的代码基线。

## 0. 结论速览

1. **主服务两平台都用 Docker 运行，差别只在转写。** `config.Folders()` 把工作目录写死为 `/data/automation/*`，翻译脚本路径写死为 `/app/scripts/...`，所以主服务离开镜像无法直接运行。macOS 保留"宿主机 MLX + 容器主服务"；Linux 改为"转写服务也进容器"。
2. **Linux 转写建议沿用现有 `/transcribe` 路径协议，由 Muse 提供一个 Linux ASR 容器。** 这样 Muse 的 Go 代码不用改，macOS 链路原样保留。容器内放两种引擎：NVIDIA 用 Qwen3-ASR（`qwen-asr`，PyTorch/CUDA），与 macOS 同一模型；CPU 默认用 faster-whisper。faster-whisper 有公开的 CPU 基准，Qwen3-ASR 没有任何公开的 CPU 速度数据。两条路都需要先做一次实机基准。
3. **mdc-ng 与 Muse 之间没有代码集成，衔接靠文件系统。** Muse 把视频交付到 `/data/automation/scraping/`，把字幕交付到 `/data/automation/subtitles/`，这是两个不同目录。mdc-ng 应该怎样监控、怎样匹配字幕，要以用户现在的实际设置为准（见第 7 节问题）。
4. **陌生用户照现有文档部署会失败。** 有三处硬伤：
   - `host_asr_server.py` 在任何真实 FastAPI 版本下都无法启动（已实测）。
   - 示例配置默认的 `openai` 翻译地址会得到 404（已实测）。
   - `docker-compose.yaml` 的挂载与代码不符，SQLite 数据库会落在容器内，重建容器即丢失。

   下文列出的其余偏差多数是个人环境残留。
5. **仓库已经是公开状态**（GitHub `weizsw/fusionn-muse` 可见性为 PUBLIC，`ghcr.io/weizsw/fusionn-muse` 可以匿名拉取，已有 `1.2.0`、`latest` 等标签）。所以"开源"的实际工作是部署产品化和文档化，不涉及首次公开。

---

## 1. 实际处理链路（已核实，按代码）

```text
qBittorrent 下载完成
  └─ 执行 python3 torrent_complete.py "%N" "%L" "%I" "%F"     （运行在 qB 所在环境）
       ├─ 分类 ≠ MUSE_CATEGORY(写死 "whisparr") → 只记日志，退出 0
       ├─ 把 qB 看到的路径前缀 HOST_INPUT_PATH 换成 Muse 看到的 CONTAINER_INPUT_PATH
       └─ POST {MUSE_URL}/api/v1/webhook/torrent {"path","name","category"}
            只有 HTTP 202 且带合法 submission_id 才算成功；不重发
Muse（容器）
  1. Submission 写入 SQLite（/data/automation/meta/fusionn-muse.db）后立即回 202
  2. 后台准备：识别番号、过滤 ≤200MB 文件、合并分段、解包 ISO 等镜像
     → 产出 Media item → 解析为新 Job 或已有 Job（同一内容不会重复建 Job）
  3. 暂存：源文件 硬链接（失败则复制）到 automation/staging/<CODE>.<ext>
  4. 移入 automation/processing/；检测已有中文字幕（文件名 / 外挂 / 内封 / 可选 OCR 硬字幕）
     └─ 有中文字幕 → "轻任务"，跳过转写和翻译
  5. 转写：POST {server_url}/transcribe {video_path, container_prefix, host_prefix, model, language}
     宿主服务把 /data 换成 host_prefix，运行 mlx-qwen3-asr，把 SRT 写在 processing/ 里视频旁边
     → Muse 把结果复制到 automation/transcriptions/<CODE>.srt（可重复翻译的持久源字幕）
  6. 翻译：容器内 PySubtrans（Custom Server 客户端，非流式 /chat/completions）
  7. 交付：先把字幕发布到 automation/subtitles/<CODE>[.<language_suffix>].srt
           再把视频移入 automation/scraping/<CODE>.<ext>
  8. 失败：视频移入 automation/failed/；可选 Apprise 通知
```

对部署有直接影响的几个细节：

- **输入目录不受约束。** 代码会创建 `Folders().Input = /data/torrents`，但没有任何地方用它校验 webhook 传入的路径。只要容器内能看到这个路径，Muse 就会处理。所以真正的契约是"qB 路径 → Muse 容器路径"这一层映射，`/data/torrents` 这个名字没有约束力。
- **硬链接只在同一挂载点内生效。** Linux 的 `link(2)` 跨挂载点会返回 EXDEV，即使两个挂载点背后是同一个文件系统[10]。Muse 遇到这种情况会退回整文件复制，功能不坏，但每个视频都会多占一份完整空间，耗时也更长（qB 做种时源文件还要保留）。作者的部署用的是单一挂载 `/Volumes/External:/data`（`docs/design/durable-job-processing.md` 有记录），下载目录和 automation 目录在同一个挂载下，所以硬链接能生效。README 示例用的是"下载目录只读挂一个、automation 另挂一个"，这正好让硬链接失效。教程应推荐"一个父目录整体挂到 `/data`"。
- **转写协议基于共享路径，不上传文件。** 转写服务必须能直接读写 Muse 看到的同一批文件。当前 macOS 方案依赖"OrbStack/Docker Desktop 容器和宿主机共享同一块磁盘"。
- **交付顺序对监控目录友好。** 字幕先发布，视频后到。视频进入 `scraping/` 时，同盘走 rename 或 link；跨盘先复制成隐藏临时文件 `.muse-copy-*`，再 link 成正式文件名。两种方式下正式文件名都是一次性出现的，监控程序不会看到写了一半的 `<CODE>.mp4`。
- **Muse API 没有任何鉴权**（webhook 和恢复操作都开放）。文档必须写明只在局域网或内网使用，不要暴露到公网。
- **容器以 root 运行**（Dockerfile 没有设置 `USER`）。在 Linux 上，产出文件归 root 所有。mdc-ng 官方示例用 `PUID/PGID=1000` 运行，可能无法移动或删除这些文件（待实机验证，见第 6 节）。

## 2. 平台运行形态

| | macOS（Apple Silicon） | Linux + NVIDIA | Linux 仅 CPU |
|---|---|---|---|
| Muse 主服务 | Docker（OrbStack 或 Docker Desktop） | Docker | Docker |
| 转写 | 宿主机 `host_asr_server.py` + `mlx-qwen3-asr`（Metal） | ASR 容器，引擎 `qwen`（CUDA） | ASR 容器，引擎 `whisper`（faster-whisper int8） |
| Muse 配置 `server_url` | `http://host.docker.internal:8766` | `http://asr:8766`（Compose 服务名） | 同左 |
| 路径映射 `host_prefix` | Mac 上对应 `/data` 的真实目录 | `/data`（两个容器挂同一目录，无需映射） | 同左 |
| 额外前提 | Python ≥3.10、ffmpeg、约 3.4 GB 内存（1.7B）[4] | NVIDIA 驱动 + NVIDIA Container Toolkit | 足够的 CPU 和内存 |
| macOS 现有链路 | **完全不变** | — | — |

macOS 支持范围建议只写 Apple Silicon：mlx-qwen3-asr 明确要求 M 系列芯片和 Metal[4]。Intel Mac 理论上可以照搬"Linux 仅 CPU"的容器方案，但不作承诺（待用户决定，见 D7）。

## 3. Linux 转写选型

### 3.1 约束

Muse 对转写端的要求来自 `internal/executor/host_asr.go` 和处理流程：

- 输入一个容器内视频路径，返回一个 Muse 能读到的 SRT 路径。
- 单个视频可能长达 1–3 小时；HTTP 超时由 `mlx_qwen3_asr.timeout_minutes` 控制（默认 180 分钟）。
- 输出必须带**句级时间轴**，因为后续翻译和交付都基于 SRT。
- 语种以日语为主（从番号识别逻辑和 `whisparr` 分类推断，**未经用户确认**）。

### 3.2 候选方案与证据

| 方案 | CPU | NVIDIA | 能否直接出 SRT | 证据与限制 |
|---|---|---|---|---|
| **Qwen3-ASR 官方 `qwen-asr`（transformers 后端）** | 代码上可用：没有 CUDA 时设备默认为 CPU[2]。**没有任何公开的 CPU 速度数据** | 官方示例使用 `cuda:0`、bfloat16，推荐 FlashAttention 2；官方 Docker 镜像需要 `--gpus all` 和 NVIDIA Container Toolkit[1][2] | 不能直接出。只返回词或字级 `time_stamps`，需要 Qwen3-ForcedAligner-0.6B（单段 ≤5 分钟，包内自动切块再拼接）[1][2]，切句成 SRT 要自己实现 | 与 macOS 同一套权重，质量最一致。包版本 0.0.6，钉死 `transformers==4.57.6`，并拉入 gradio、flask 等较重的依赖[3]。官方未给出最低显存 |
| Qwen3-ASR + vLLM（`qwen-asr-serve`） | 不可用 | 可用，需要 CUDA[2] | vLLM 转写接口**不支持 srt/vtt**，只有 json/verbose_json；默认拒绝超过 600 秒或 25 MB 的音频（可用环境变量放宽）[7] | 吞吐高，但长视频和 SRT 都要额外处理，对单机单任务的 Muse 收益有限 |
| **faster-whisper（CTranslate2）** | 可用，int8；官方基准：13 分钟音频、small 模型、i7-12700K 8 线程，int8 耗时 1 分 42 秒，内存 1.5 GB[5] | 可用，需要 CUDA 12 + cuDNN 9；官方基准：large-v2 在 RTX 3070 Ti 上，fp16 耗时 1 分 03 秒、显存 4.5 GB；int8 显存 2.9 GB[5] | 返回分段时间轴，可开 VAD，转 SRT 很简单[5] | 成熟、MIT 许可。这个项目以前通过 VideoCaptioner 用过 FasterWhisper，后来停用。停用原因（质量还是维护）**没有记录**，需要用户说明（D2） |
| speaches（faster-whisper 的 OpenAI 兼容服务） | 有官方 `latest-cpu` 镜像 | 有 `latest-cuda` 等镜像[6] | 支持 `response_format=srt`，长文件靠 VAD 分段[6] | 上传式接口（multipart），与 Muse 的路径协议不同，需要新写 Go 适配器。最新发布仍是 `v0.9.0-rc.3`（2025-12）[6] |
| sherpa-onnx 的 Qwen3-ASR（ONNX） | 可用 | 可用 | **不输出时间戳**[8] | 不适合直接做字幕，排除 |
| 云端 ASR API | — | — | 视服务而定 | 有费用和内容合规风险（成人内容可能违反服务条款），只作为用户自选项，不作默认 |

另有一条旁证：mlx-qwen3-asr 自报，在 80 秒片段上 MLX 版错误率 10.59%，PyTorch 参考实现为 17.99%，原因是 MLX 版在停顿处切块，参考实现整段解码[4]。这说明**在 Linux 上跑同一模型，长音频切块方式同样会影响质量**。Linux 端的 Qwen 实现应当复用类似的停顿切块，不能整段喂给模型。这一点待实机验证。

### 3.3 架构取舍：Muse 怎么对接 Linux 转写

**方案 A（推荐首发）：沿用现有 `/transcribe` 路径协议，Muse 自带 Linux ASR 镜像**

- Linux ASR 容器实现与 `host_asr_server.py` 相同的请求和响应结构，与 Muse 挂载同一个 `/data`，`host_prefix=/data`。
- 容器内通过环境变量选择引擎：`qwen`（CUDA）或 `whisper`（CPU，也可以跑在 CUDA 上）。
- 好处：Muse 的 Go 代码和配置结构不用改，旧任务的设置快照不受影响，macOS 链路零改动。
- 代价：要维护一个 Python 服务。Qwen 的"词级时间戳 → 句级 SRT"逻辑要自己实现，可以参考 mlx-qwen3-asr（Apache-2.0）的 SRT 输出做法。

**方案 B（以后再议）：Muse 增加 OpenAI 兼容的上传式转写客户端**

- macOS 可以直接用上游的 `mlx-qwen3-asr serve`（支持 srt，单文件上限 2 GB / 8 小时，长音频官方建议走异步接口）[4]；Linux CPU 可以直接用 speaches[6]。
- 好处：不用自己维护 ASR 服务。
- 代价：要新写 Go 适配器，并先用 ffmpeg 抽出音频再上传。Linux 上的 Qwen 仍然没有现成的"能出 SRT 的服务"（vLLM 不出 SRT[7]），所以仍然要自己写。而且这会改变 macOS 现有链路。

**推荐 A。** 用户要求保留现有 macOS 路径，A 的改动面也最小。B 等方案 A 运行一段时间后再评估。

配置命名问题：`pipeline.provider: mlx_qwen3_asr` 和配置段 `mlx_qwen3_asr:` 在 Linux 上名不副实。provider 值会写进任务的设置快照，所以改名必须保留旧值作为别名。建议首发**不改名**，文档里说明"这是宿主或独立 ASR 服务协议，名字沿用历史"。改名留作后续工单。

### 3.4 实机才能确认的事项（建议作为第一张实施工单）

在真实 Linux 机器上，用同一段 1–2 小时、以日语为主的样片测量以下内容：

| 测量 | CPU 机器 | NVIDIA 机器 |
|---|---|---|
| Qwen3-ASR 0.6B / 1.7B（transformers + ForcedAligner）实时率与峰值内存或显存 | ✔（预期很慢，需要量化） | ✔ |
| faster-whisper large-v3 / turbo / medium（int8 或 fp16）实时率与内存 | ✔ | ✔ |
| 与 macOS MLX 输出对比日语字幕质量（人工抽查，或以 MLX 结果为参照算 CER） | ✔ | ✔ |
| 最小可用显存 / 内存，用于文档里的"硬件要求" | ✔ | ✔ |

判定规则（建议，属于本方案的自定判断）：如果 Qwen 在 CPU 上处理 2 小时视频超过 6 小时，CPU 首发默认用 faster-whisper，Qwen-CPU 标为"实验"。NVIDIA 默认用 Qwen，除非实测质量或显存不可接受。

## 4. mdc-ng 衔接

**已核实：** Muse 代码里没有任何 mdc-ng 相关内容。Muse 的产出是：

- 视频：`/data/automation/scraping/<CODE>.<ext>`（文件名已清洗为番号）
- 字幕：`/data/automation/subtitles/<CODE>[.<subtitle.language_suffix>].srt`；轻任务直接沿用外挂字幕的原扩展名（例如 `.ass`）
- 字幕先于视频出现，视频文件名一次性出现（见第 1 节）

**有文档依据[9]：** mdc-ng 提供目录监控（"性能模式"实时监听，"兼容模式"定时扫描）、五种整理模式（硬链接、复制、移动、软链接、原地整理），能"自动整理视频自带字幕文件，支持本地字幕库匹配"。它以 Docker 部署，有 `PUID/PGID`，`/config` 存配置，`/media` 可映射多个媒体目录。mdc-ng 仓库只公开 README 和图标，看不到源码，所以**字幕库的命名规则、是否保留语言后缀，都无法从源码确认**。README 没有提到触发刮削的 HTTP 接口。

**推断的衔接方式（需要用户确认）：** mdc-ng 监控 `scraping/`，选"移动"或"硬链接"模式整理到媒体库，把 `subtitles/` 设为本地字幕库。文档要写清楚：

1. mdc-ng 容器和 Muse 挂载同一个宿主父目录。用硬链接模式时，源目录和目标目录也要在同一个挂载点内。
2. `subtitle.language_suffix` 怎么设（例如 `zh-CN` 或 `chs`），才能在 mdc-ng 整理后被 Emby/Jellyfin 识别为中文字幕。这需要实测。
3. 文件权限：Muse 以 root 写文件，mdc-ng 以 1000:1000 运行时能否移动这些文件。
4. 如果 mdc-ng 用"移动"模式，`scraping/` 会被清空，这不影响 Muse 的重新翻译。重新翻译只需要 `transcriptions/` 里的源字幕，不需要视频还在（README 已有说明）。但新的翻译结果只会写到 `subtitles/`，**不会自动进入 mdc-ng 已经整理好的媒体库**。教程要说明重新翻译之后怎样同步到媒体库。

## 5. 现有文档与配置的缺口

### 5.1 会直接导致陌生用户部署失败的（阻断级）

| # | 问题 | 证据 | 建议处理 |
|---|---|---|---|
| B1 | `host_asr_server.py` 写的是 `from fastapi import ... JSONResponse ...`，但 FastAPI 并不从顶层导出 `JSONResponse`（应从 `fastapi.responses` 导入）。服务启动即报 ImportError。`host_server.py`（已停用的 DuoSubs）同样有这个问题 | **已实测**：FastAPI 0.100.0、0.115.0、0.162.0 都报 `ImportError: cannot import name 'JSONResponse'`。现有测试用桩模块替代了 fastapi，所以没发现。作者本机能跑，推测是在用这次提交（86773a9）之前的旧副本，**待作者确认** | 代码修复，并补一个用真实 fastapi 导入的测试 |
| B2 | 示例配置 `translate.provider: "openai"` 映射的地址是 `https://api.openai.com`（没有 `/v1`），PySubtrans 会请求 `.../chat/completions` | **已实测**（未带凭据的 POST）：`api.openai.com/chat/completions` 返回 404，`/v1/...` 返回 401（地址存在）。openrouter、groq、fireworks 的内置地址同样是 404；deepseek、together、siliconflow 的地址存在 | 短期：文档只推荐 `provider: custom` 加上带 `/v1` 的完整地址。长期：修正内置地址表（代码工单） |
| B3 | `docker-compose.yaml` 挂载的是 `/data/input`、`/data/finished`、`/data/subtitles`，代码一个都不用；注释说"queue 不持久化会丢任务"，与 SQLite 实现不符。实际的数据库 `/data/automation/meta/...` 没有挂载，会落在容器可写层，**重建容器即丢失全部任务历史** | 对比 `config.Folders()` 和 `queue.db_path` 默认值 | 按第 1 节的单挂载布局重写，改用 `image: ghcr.io/weizsw/fusionn-muse` |
| B4 | `torrent_complete.py` 的分类（`whisparr`）、路径映射（`/Volumes/External → /data`）都是常量，只能改源文件；`MUSE_URL` 的默认值是作者的局域网 IP | 读代码 | 改为环境变量可配置，默认值改成中性值。这也是 agent 能自动配置的前提 |

### 5.2 个人环境残留和文档内部矛盾（误导级）

- README 的镜像地址写成 `ghcr.io/your-username/...`；实际镜像 `ghcr.io/weizsw/fusionn-muse` 已经存在，可以匿名拉取。
- README 端口写 `-p 8080:8080`，示例 curl 却用 `localhost:4444`（作者的个人映射）。
- README 示例 `custom_server: "http://192.168.50.135:8317/v1"`、数据库说明里的 `/Volumes/External`、design 文档里的"active deployment"，都是个人环境。
- README 说"compose 设置了 TZ，默认 Asia/Shanghai"，compose 文件里却是 `Asia/Singapore`。
- `config.example.yaml` 末尾的 API 列表（`/queue`、`/jobs`、`/retry`…）都是已删除的路由。
- README 的"Folder Structure"把 `/data/torrents` 写成输入目录，但它并没有约束力（见第 1 节）。
- README 正文超过 30 KB，安装说明与恢复 API、升级安全、日志规则混在一起，新用户找不到起点。
- 没有讲 qB 跑在 Docker 里的情况：脚本要放进 qB 容器能访问的目录，路径用 qB 容器内的路径；linuxserver 的 qB 镜像自带 python3[11]。
- 没有讲 macOS 转写服务怎样常驻（例如 launchd）。目前需要从仓库根目录手动启动，因为脚本里有 `from scripts.logfmt import`。
- 没有记录 `FUSIONN_MUSE_<段>_<键>` 环境变量覆盖配置的能力。**已实测**：`FUSIONN_MUSE_TRANSLATE_API_KEY` 能覆盖 YAML 里的空 `api_key`。有了它，agent 部署时可以把密钥放在权限 600 的 `.env` 里，不写进配置文件。
- `AGENTS.md` 是给代码贡献者看的行为准则，不是部署指导。两者应该分开。

### 5.3 开源前顺带检查的结果

- 用常见格式扫描整个 git 历史，只发现测试用的假 key `sk-abcdefghijklmnop`，没有发现真实凭据。历史里有局域网 IP、本机路径和两个提交邮箱。仓库已经公开，这些不构成新暴露，只提示一下。
- LICENSE 是 GPL-3.0。
- `build.yml` 已经构建 amd64/arm64 双架构镜像并推送到 GHCR 和 Docker Hub（Docker Hub 用户名来自仓库 secret）。

## 6. 文档方案

### 6.1 文档结构

```text
README.md                      入口：是什么、架构图、平台矩阵、5 分钟上手、文档导航（主语言见 D1）
docs/
  deploy/
    macos.md                   Apple Silicon：Docker 主服务 + 宿主 MLX 转写（含常驻）
    linux.md                   Linux：Compose（Muse + ASR 容器），CPU 和 NVIDIA 两套配置
  integrations/
    qbittorrent.md             原生 qB 和 Docker qB 两种情况；分类、路径映射、手动重发
    mdc-ng.md                  监控目录、字幕库、语言后缀、权限、重新翻译后的同步
  configuration.md             配置项说明；环境变量覆盖；LLM 服务地址要带 /v1
  operations.md                任务查看、重试、恢复、重新翻译（从现有 README 迁出）
  troubleshooting.md           症状 → 原因 → 检查命令
  agent-deploy.md              给 agent 的部署说明（见 6.3）
examples/
  compose.macos.yaml / compose.linux-cpu.yaml / compose.linux-nvidia.yaml
  config.macos.yaml / config.linux.yaml
```

README 只保留入口内容。现有 README 里的恢复、API、升级安全等长段落原样迁到 `operations.md`，避免改写已有的准确内容。

### 6.2 各文档必须讲清楚的内容

- **统一目录布局**（所有教程用同一套，示例名称可以由用户替换）：

  ```text
  <数据根>/                 → 所有容器都挂成 /data（mdc-ng 按自己的要求挂）
    downloads/              qB 保存位置
    automation/{staging,processing,scraping,subtitles,transcriptions,meta,failed}
    library/                mdc-ng 整理后的媒体库（可选）
  ```

- **三层路径对照表**：qB 看到的路径 → Muse 容器路径 → 转写服务看到的路径。macOS 上第三层是 Mac 本地路径，Linux 上就是 `/data`。
- **端到端验证步骤**：
  1. `GET /api/v1/health` 和 `/api/v1/version`；
  2. 转写服务 `GET /health`；
  3. 用 `dry_run: true` 走一遍文件流程，不调用 ASR 和 LLM；
  4. 手动执行一次 `torrent_complete.py`，看到 202 和 `submission_id`；
  5. `GET /api/v1/tasks/submission/<id>` 跟踪任务；
  6. 确认 `subtitles/` 和 `scraping/` 出现产出，mdc-ng 完成整理。
- **安全提示**：Muse API 没有鉴权，不要暴露到公网；密钥放在 `.env` 里并设权限 600。
- **适用范围说明**：番号识别和"小于等于 200 MB 视为广告"等规则是为特定类型内容设计的。文档需要如实说明这一点，用词由用户决定（D8）。

### 6.3 Agent 部署说明（`docs/agent-deploy.md`）

定位：用户对自己的 agent 说"按 fusionn-muse 仓库的 docs/agent-deploy.md 帮我部署"，agent 在目标机器上照着执行。写成通用 Markdown，不绑定某一家 agent 产品。

**一、需要向用户收集的信息**（缺一项就停下来询问，不要猜）

| 项 | 用途 |
|---|---|
| 平台和转写引擎：macOS-MLX / Linux-NVIDIA-Qwen / Linux-CPU-Whisper | 选择 compose 模板 |
| 数据根目录（须与 qB 下载目录在同一文件系统） | 挂载和硬链接 |
| qB 的运行方式（原生 / Docker）、它看到的下载路径、分类名 | 路径映射和分类过滤 |
| 是否允许 agent 通过 qB WebUI API 写入"完成时运行"设置（需要 WebUI 地址和账号） | qB 偏好里只有一个"完成时运行"程序位，可以通过 WebUI API 的 `autorun_enabled`、`autorun_program` 读写[12] |
| LLM 服务的完整地址（含 `/v1`）、模型名、API key、目标语言 | 翻译 |
| 转写模型（0.6B / 1.7B；whisper 模型名） | 资源占用 |
| 是否用 mdc-ng，它的监控目录和字幕库路径 | 衔接 |
| 时区、对外端口、可选的 Apprise | 运行参数 |

**二、部署前检查**（每项都给出可执行命令和判定标准）

- 操作系统和架构；macOS 必须是 Apple Silicon。
- 已安装 Docker 和 Compose v2。**未安装时，agent 不能自行安装，应交还给用户。**
- macOS：Python ≥3.10、ffmpeg 已安装，能 `pip install mlx-qwen3-asr`。
- NVIDIA：宿主机 `nvidia-smi` 正常；`docker run --rm --gpus all <cuda 基础镜像> nvidia-smi` 正常（证明 Container Toolkit 可用）。
- 数据根和下载目录在同一设备上（比较 `stat` 的设备号）；剩余空间足够。
- 端口没有被占用。
- 能连通 LLM 服务（用 key 请求 `GET <base>/models`；不打印 key）。
- 从 qB 所在环境能访问 Muse 地址（qB 在 Docker 里时，从 qB 容器内执行 curl）。

**三、执行步骤**：写入配置（已有文件先备份，不覆盖）→ 启动 compose →（macOS）创建 venv、安装 ASR、配置常驻 → 放置 qB 脚本并设置 `MUSE_URL`、分类、路径映射 → 配置 qB "完成时运行"。如果 qB 里已经有别的程序，不要覆盖，先告诉用户，再决定是否串联执行。

**四、部署后验证**：执行 6.2 的端到端验证，并把每一步的结果报告给用户。

**五、约束**：
- 不改动用户已有的下载文件。
- 不把密钥写进日志或聊天。
- 每次修改 qB 设置前，先展示当前值和要写入的新值。
- 失败时说明停在哪一步，并给出回滚方法（`docker compose down`，恢复备份的配置）。

需要的授权：目标机器的 shell 权限；安装 NVIDIA 驱动或 Toolkit 需要 sudo（建议由用户自己完成）；可选的 qB WebUI 账号；用户自己的 LLM API key。

## 7. 待用户决定的事项

| # | 决策 | 建议 | 影响 |
|---|---|---|---|
| D1 | 文档主语言 | 中文为主（用户、mdc-ng 社区和目标用户都以中文为主），README 顶部附一段英文简介 | 全部文档 |
| D2 | Linux CPU 的默认引擎 | 先按 faster-whisper 设计，基准测试后再定；需要用户说明当初停用 FasterWhisper 的原因 | ASR 镜像内容 |
| D3 | Linux 首发时的支持级别 | NVIDIA 和 CPU 都写进文档；没有实机验证过的标为"实验"。**需要用户确认有没有 Linux NVIDIA 机器可以做测试** | 首发范围和验证工单 |
| D4 | mdc-ng 的实际设置 | 请用户提供：监控目录、整理模式、字幕库路径、媒体库路径、是否接 Emby、现在 `language_suffix` 的值 | mdc-ng 教程能否写准确 |
| D5 | 发布方式 | Muse 继续用 `ghcr.io/weizsw/fusionn-muse`；Linux ASR 镜像分 CPU 和 CUDA 两个标签，同仓发布；以新的 minor 版本号首发 | CI 工作流 |
| D6 | Agent 说明的形式 | 仓库内通用 Markdown；以后需要时再封装成特定 agent 的技能 | 维护成本 |
| D7 | macOS 支持范围 | 只正式支持 Apple Silicon；Intel Mac 不作承诺 | 文档承诺 |
| D8 | 内容定位的表述 | 如实说明识别规则针对的内容类型，默认分类名可以配置 | README 措辞 |

## 8. 建议的后续工单（按依赖顺序）

1. **修复阻断项**（B1–B4）：修正 host_asr_server 的导入并补测试；重写 compose；`torrent_complete.py` 改成环境变量配置；更新示例配置并移除失效的 API 列表。B2 的长期修复（内置地址表）可以同时做，也可以单独做。
2. **Linux 转写实机基准**（第 3.4 节）：只产出数据和结论，作为第 3 项的输入。
3. **Linux ASR 镜像**（方案 A）：实现与 `/transcribe` 相同的契约，支持 `qwen` 和 `whisper` 两种引擎，提供 CPU 和 CUDA 两个镜像，并接入 CI。
4. **文档重写**：按第 6 节的结构，取决于 D1、D4。
5. **Agent 部署说明**：在第 1、3、4 项完成后编写，用一台全新机器实际跑一遍。
6. （可选）provider 改为通用名称并保留旧名作为别名；容器以非 root / PUID 运行；给 API 加可选 token。

## 9. 本次做过的验证

- 通读：`processor.go`、`host_asr.go`、`llm_subtrans.go`、`config.go`、`main.go`、`handler.go`、`fileops.go`、`resolver.go`、`torrent_complete.py`、`host_asr_server.py`、Dockerfile、compose、示例配置、CI。
- 实测：
  - 在 3 个 FastAPI 版本下导入失败；
  - 7 个内置 LLM 服务地址的 HTTP 状态；
  - 用临时测试确认环境变量能覆盖 `translate.api_key`（测试已删除，未留在仓库里）；
  - `.dockerignore` 的匹配结果；
  - GHCR 镜像可以匿名拉取，以及现有标签。
- `make lint`：0 issues。`make test`：Go 测试全部通过；Python 132 个测试通过，跳过 11 个（需要镜像环境的测试）。
- 未做：任何 Linux 实机转写、mdc-ng 实际联调、Docker 构建（本机 Docker 守护进程没有运行）。

## 参考

1. QwenLM/Qwen3-ASR README（Apache-2.0）：https://github.com/QwenLM/Qwen3-ASR
2. DeepWiki 对 QwenLM/Qwen3-ASR 源码的问答（AI 摘要，属二手来源）：CPU 回退、不直接输出 SRT、`qwen-asr-serve` 基于 vLLM、Docker 使用 `--gpus all`。https://deepwiki.com/QwenLM/Qwen3-ASR
3. PyPI `qwen-asr` 0.0.6 的元数据（依赖列表）：https://pypi.org/project/qwen-asr/
4. mlx-qwen3-asr README 与 docs/server/README.md：https://github.com/moona3k/mlx-qwen3-asr
5. SYSTRAN/faster-whisper README（基准测试、CUDA 12 / cuDNN 9）：https://github.com/SYSTRAN/faster-whisper
6. speaches-ai/speaches（DeepWiki 摘要 + 发布页 v0.9.0-rc.3）：https://github.com/speaches-ai/speaches
7. vLLM 转写接口（DeepWiki 摘要：支持的格式，以及 600 秒 / 25 MB 默认上限）：https://deepwiki.com/vllm-project/vllm
8. k2-fsa/sherpa-onnx 的 Qwen3-ASR 支持（DeepWiki 摘要）：https://deepwiki.com/k2-fsa/sherpa-onnx
9. mdc-ng README：https://github.com/mdc-ng/mdc-ng
10. Linux `link(2)` 中关于 EXDEV 的说明：https://man7.org/linux/man-pages/man2/link.2.html
11. linuxserver/docker-qbittorrent Dockerfile（安装了 python3）：https://github.com/linuxserver/docker-qbittorrent
12. qBittorrent `src/webui/api/appcontroller.cpp`（`autorun_enabled` / `autorun_program` 偏好项）：https://github.com/qbittorrent/qBittorrent
