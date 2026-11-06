# 首次部署验证与开源资料承接报告

本文档记录 **Fusionn-Muse** 开源资料承接成果、第 3 轮 Reviewer 意见复核结论，以及在隔离模拟接口下的首次部署全链路自检验证。

---

## 一、承接基线与成果核实

本次交付完整承接自分支 `tds/conv-01a1afff-d527-7298-b439-a43bb1e21e04`（提交 `b88691c` 及前序提交 `b4839e4`、`ef9ee36`、`8d245d0`），涵盖全部开源整理文档、配置示例与脚本修正，无从头重写：

1. **`README.md`**：
   - 完整阐述系统架构与工作原理：qBittorrent Webhook 触发 ➔ Muse 广告过滤/代码识别/MLX ASR转录/LLM翻译 ➔ 输出 `scraping` ➔ mdc-ng 兼容轮询监控刮削 ➔ 115strm 云端串流与本地 `.strm` 生成 ➔ Emby 极速原画秒开。
   - 明确平台支持状态：当前生产实测支持 macOS Apple Silicon；Linux 专项适配推进中；开源协议明确为 GNU GPLv3（与 `LICENSE` 一致）。
2. **`docs/deployment/macos-deployment.md`**：
   - 针对 macOS (Apple Silicon M系列) 的生产部署指南，涵盖目录层级、宿主机 ASR Python 虚拟环境与 launchd 常驻守护进程配置、Docker Compose 编排、qBittorrent 回调及分级验证流程。
3. **`docs/deployment/linux-deployment.md`**：
   - 客观阐述 Linux 平台的当前支持能力：原生支持媒体扫描、广告过滤、多段合并、硬字幕抽样检测识别与直接放行；明确生肉视频本地 ASR 转录正处于专项适配阶段，首批将同时覆盖 NVIDIA GPU 与纯 CPU。
4. **`docs/deployment/mdc-ng-integration.md`**：
   - 跨服务路径映射矩阵，重点强调 mdc-ng 必须开启“兼容监控模式（watcher_kind: 1）”，避免 Docker 卷绑定挂载时丢失 inotify 文件移动事件。
5. **`docs/deployment/agent-deployment.md`**：
   - 专为 AI Agent（Claude Code, Pi, Cursor, Codex 等）设计的 8 步自动化部署规范 Playbook。
6. **配置示例与脚本修正**：
   - `docker-compose.yaml`：标准化镜像、卷映射与参数。
   - `config/config.example.yaml`：对齐最新的 `TaskDetail` 顶层字段与 API 端点说明。
   - `host_asr_server.py` 与 `host_server.py`：对齐现代 FastAPI，修正 `JSONResponse` 导入路径。
   - `scripts/torrent_complete.py`：修正默认 Muse 地址，增强 `HOST_INPUT_PATH` 与 `CONTAINER_INPUT_PATH` 环境变量覆盖支持。

---

## 二、审查意见复核与深度边界修复

### 1. 核心问题复核分析

在复查前序交付及审查反馈后，明确了三大核心阻碍与边界风险：
1. **固定番号查询导致同名旧任务假阳性**：两份教程若直接按固定番号查询 `GET /api/v1/tasks/job/SNOS-411`，当系统存在同名历史已完成任务时，若本次 Submission 在准备阶段（文件过小/非媒体/路径错误）终止，命令仍会误读历史任务的 `completed` 状态，并因旧产物仍留在磁盘而误判为“验证成功”。必须先查询本次 Submission，处理异常状态，待解析出 `canonical_task` 后按动态关联的 Job ID 跟踪当前执行。
2. **缺乏轮询等待与异常阻断**：Webhook 接收后，后台媒体准备由 Worker 异步执行（耗时通常为数十毫秒至数秒）。若立即单次请求 `GET /api/v1/tasks/submission/${SUBMISSION_ID}`，此时 `canonical_task` 尚未生成（为 `null`）。脚本提取的 `JOB_ID` 为空，紧接着以空 ID 请求 `/api/v1/tasks/job/` 导致 404；若未启用 `set -e`，单次查询更会导致错误静默漏过、误读旧文件。
3. **已有字幕放行 (`is_light=true`) 与残留旧字幕导致的假阳性**：真实处理器在命中硬字幕 OCR 或内嵌字幕时，会将视频标记为 `is_light=true` 直接放行至 `scraping` 目录供刮削，此时不会生成新的转录或翻译字幕（`translated_path` 为空）。若验证命令仅检查固定字幕路径是否存在且非空，磁盘上残留的历史旧字幕会导致放行任务被误报为“ASR 转录与 LLM 翻译全链路验证通过”。

### 2. 针对性修复实施

对 `docs/deployment/agent-deployment.md` 与 `docs/deployment/macos-deployment.md` 进行了全面强化与对齐：
- **两份教程统一引入有界轮询与严格错误阻断**：
  - 步骤一轮询 `GET /api/v1/tasks/submission/${SUBMISSION_ID}`（最多 30 次，每次间隔 2 秒），等待后台准备完毕并解析出 `canonical_task.id`。
  - 遇到 `failed`、`ignored`、`interrupted`、HTTP 查询异常（500/404）或超时未解析出关联 Job 时，立即打印具体错误并 `exit 1`。
  - 步骤二按动态解析出的 `${JOB_ID}` 轮询 Job 执行状态，区分 `transcribing`、`translating`，遇到 `failed`、`interrupted` 或接口异常立即退出。
- **绑定动态产物路径，彻底消除残留旧字幕污染**：
  - 提取顶层与 Job 内的 `is_light` 标识与 `translated_path`。
  - 若 `is_light == true`，明确提示该样本因已有中文字幕而直接放行，并未经过 ASR/LLM 翻译，并在全链路验证时明确报错终止，指导用户使用未压制字幕的生肉视频重新检验。
  - 若 `is_light == false`，必须校验 `translated_path` 非空，并将其映射为宿主机物理路径 `HOST_SUB_PATH`，仅针对本次动态生成的字幕文件执行 `[ -s ... ]` 物理校验。

---

## 三、隔离模拟接口真实命令执行验证矩阵

为确保测试真实反映文档交付成果，`scripts/test_deployment_verification.py` 摒弃硬编码脚本，改为**通过正则直接从 `docs/deployment/agent-deployment.md` 与 `docs/deployment/macos-deployment.md` 提取端到端验证的 Markdown Bash 代码块**，并在隔离模拟接口与预留历史同名旧文件污染的环境下逐项执行。

测试矩阵覆盖两份教程各自的 9 大场景（共 18 项自动化用例）：

| 场景编号 | 测试场景 / 条件 | 模拟条件 | 历史同名旧产物 | 预期行为 | Agent 教程实测 | macOS 教程实测 |
|:---:|:---|:---|:---:|:---|:---:|:---:|
| **1** | **正常全链路成功 (`success`)** | 准备就绪 ➔ 关联 Job ➔ ASR/LLM 完成 (`is_light=false`, 有 translated_path) | 旧视频、旧字幕 | 成功提取 Job ID，等待完成，绑定校验动态字幕产物，返回 0 | ✅ PASS (ExitCode: 0) | ✅ PASS (ExitCode: 0) |
| **2** | **已有字幕放行防旧字幕污染 (`light_bypass`)** | 命中硬字幕直接放行 (`is_light=true`, 无 translated_path) | 磁盘残留旧同名字幕 | **识别为放行流转，不误认旧字幕为本次产物，明确报错 exit 1** | ✅ PASS (ExitCode: 1) | ✅ PASS (ExitCode: 1) |
| **3** | **准备失败防旧任务污染 (`prep_failed`)** | 视频过小/校验失败，Submission `status: failed` | `SNOS-411` 历史已完成 | **立即报错终止 exit 1，绝不访问旧 Job，绝不误报成功** | ✅ PASS (ExitCode: 1) | ✅ PASS (ExitCode: 1) |
| **4** | **准备中断 (`interrupted`)** | Worker 中断，Submission `status: interrupted` | 历史已完成 | 捕获中断状态并立即报错 exit 1 | ✅ PASS (ExitCode: 1) | ✅ PASS (ExitCode: 1) |
| **5** | **非媒体过滤 (`ignored`)** | 过滤非视频，Submission `status: ignored` | 历史已完成 | 捕获忽略状态并立即报错 exit 1 | ✅ PASS (ExitCode: 1) | ✅ PASS (ExitCode: 1) |
| **6** | **超时无关联 (`unlinked`)** | 持续处于 preparing，`canonical_task` 为空 | 历史已完成 | 达到轮询上限后报错“超时未解析出关联 Job ID” exit 1 | ✅ PASS (ExitCode: 1) | ✅ PASS (ExitCode: 1) |
| **7** | **查询接口异常 (`query_500`/`404`)** | 接口报错 HTTP 500 / 404 | 历史已完成 | curl 异常被立即捕获，报错 exit 1 | ✅ PASS (ExitCode: 1) | ✅ PASS (ExitCode: 1) |
| **8** | **关联 Job 执行失败 (`job_failed`)** | 关联 Job 运行中报错 (`status: failed`) | 历史已完成 | 读取本次 Job 顶层错误，打印并 exit 1 | ✅ PASS (ExitCode: 1) | ✅ PASS (ExitCode: 1) |
| **9** | **字幕物理文件缺失 (`artifacts_missing`)** | Job 状态为 completed，但字幕文件未生成或为空 | 历史已完成 | 动态路径非空检查失败，报错 exit 1 | ✅ PASS (ExitCode: 1) | ✅ PASS (ExitCode: 1) |

全部 18 项用例均直接执行两份教程的实际 Markdown 代码块，测试完全通过。

---

## 四、门禁验证与结果

在本地工作树执行仓库门禁：

1. **代码检查 (`make lint`)**：
   ```bash
   golangci-lint run ./...
   ```
   **结果**：0 issues，完全通过。

2. **单元与集成测试 (`make test`)**：
   - Go 核心队列、处理器与工具运行测试套件全部 PASS。
   - Python 单元测试套件全部通过（执行 151 项全部 PASS，跳过 11 项仅限 Docker 镜像环境的测试）。
   - 直接提取并执行两份教程 Markdown 代码块的 18 项部署验证测试全部 PASS。

---

## 五、后续演进确认

按照用户选定并批准的推进路线：
- 当前开源资料整理与部署自检验证已全部闭环。
- **Linux 运行时支持**（同时覆盖 NVIDIA GPU 与纯 CPU 方案）将在此文档任务合并后，由下一个 Builder 任务正式实施。
