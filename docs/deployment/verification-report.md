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

## 二、第 3 轮审查意见复核与问题修复

### 1. 核心问题复核分析

第 3 轮 Reviewer 提出的核心阻碍为：
> **[P2] 端到端检查仍未跟踪本次 Submission 关联的 Job**：
> 两份教程若直接按固定番号查询 `GET /api/v1/tasks/job/SNOS-411`，当系统存在同名历史已完成任务时，若本次 Submission 在准备阶段（文件过小/非媒体/路径错误）终止，命令仍会误读历史任务的 `completed` 状态，并因旧产物仍留在磁盘而误判为“验证成功”。必须先查询本次 Submission，处理异常状态，待解析出 `canonical_task` 后按动态关联的 Job ID 跟踪当前执行。

在对前序提交 `b88691c` 进行深入执行流排查后，发现两个进一步的边界隐患：
1. **未设置轮询等待机制**：Webhook 接收后，后台准备工作由后台 Worker 异步执行（耗时通常为数十毫秒至数秒）。若立即单次请求 `GET /api/v1/tasks/submission/${SUBMISSION_ID}`，此时 `status` 为 `processing`、`stage` 为 `preparing`，`canonical_task` 尚未生成（为 `null`）。脚本提取的 `JOB_ID` 为空，紧接着以空 ID 请求 `/api/v1/tasks/job/` 导致 404，在正常成功的路径上产生“假阴性”报错；若未启用 `set -e`，还可能漏过错误。
2. **异常与超时未阻断**：若接口发生 500/404 错误、任务被标记为 `ignored`、`interrupted`，或长时间无法关联 Job，原脚本缺乏有界超时与显式阻断退出机制。

### 2. 针对性修复实施

对 `docs/deployment/agent-deployment.md` 与 `docs/deployment/macos-deployment.md` 进行了全面强化：
- **`agent-deployment.md`**：
  - 步骤一引入有界轮询循环（最多 30 次，每次间隔 2 秒），等待后台准备完毕并解析出 `canonical_task.id`。
  - 遇到 `failed`、`ignored`、`interrupted` 或 HTTP 查询异常时，立即打印具体阶段与错误信息并 `exit 1`。
  - 超时未解析出 `canonical_task.id` 时显式阻断，绝不使用空 ID 或回退至固定番号查询。
  - 步骤二按解析出的 `${JOB_ID}` 轮询 Job 执行状态，区分 `transcribing`、`translating`，遇到 `failed`、`interrupted` 立即退出。
  - 步骤三严格校验 `${STATUS} == "completed"`，并对 `scraping` 视频与 `subtitles` 字幕文件执行 `[ -s ... ]` 非空物理校验。
- **`macos-deployment.md`**：
  - 在步骤 3 与步骤 4 中明确区分“准备阶段轮询”与“已完成准备”，提供显式提取脚本 `JOB_ID=$(curl ... | jq -r '.canonical_task.id // empty')`，严禁操作者留空或按固定番号查询。

---

## 三、隔离模拟接口多分支验证矩阵

为了彻底检验两份教程中的命令与验证逻辑，我们在隔离环境下启动模拟 Muse 服务，并构建了涵盖历史同番号数据污染的 8 大分支测试（固化在 `scripts/test_deployment_verification.py` 中）：

| 测试用例 / 场景 | 模拟条件 | 历史同番号 Job 状态 | 预期行为 | 实测结果 | 结论 |
|:---|:---|:---:|:---|:---:|:---:|
| **1. 正常成功链路 (`success`)** | Webhook 202 ➔ 准备中 ➔ 关联 Job ➔ 执行完成 ➔ 双产物就绪 | 无影响 | 正常解析关联 Job ID，等待 completed，物理文件校验通过，返回 0 | ExitCode: 0, Output: PASS | ✅ 符合预期 |
| **2. 准备失败防污染 (`prep_failed`)** | 视频小于 200MB，Submission 准备失败 (`status: failed`) | `SNOS-411` 历史已完成且有旧文件 | **必须立即报错终止，绝不访问旧 Job，绝不误报成功** | ExitCode: 1, 未访问旧Job | ✅ 彻底解决第3轮阻碍 |
| **3. 准备中断 (`interrupted`)** | 服务重启或 Worker 中断 (`status: interrupted`) | `SNOS-411` 历史已完成 | 捕获中断状态，打印错误并 `exit 1` | ExitCode: 1, 未访问旧Job | ✅ 符合预期 |
| **4. 非媒体过滤 (`ignored`)** | 文件名不合规或被过滤 (`status: ignored`) | `SNOS-411` 历史已完成 | 捕获忽略状态，打印说明并 `exit 1` | ExitCode: 1, 未访问旧Job | ✅ 符合预期 |
| **5. 准备超时无关联 (`unlinked`)** | 持续处于 preparing，`canonical_task` 为空 | `SNOS-411` 历史已完成 | 达到重试上限后报错“超时未解析出关联 Job ID”并 `exit 1` | ExitCode: 1, 未访问旧Job | ✅ 符合预期 |
| **6. 提交查询异常 (`query_500`/`404`)** | 接口报错 HTTP 500 或连接异常 | `SNOS-411` 历史已完成 | curl 异常被立即捕获，报错终止 | ExitCode: 1, 未访问旧Job | ✅ 符合预期 |
| **7. 关联 Job 执行失败 (`job_failed`)** | 成功关联 Job，但 LLM 翻译或 ASR 异常 | `SNOS-411` 历史已完成 | 读取本次 Job 的顶层 `.error`，打印并 `exit 1` | ExitCode: 1, 未访问旧Job | ✅ 符合预期 |
| **8. 物理产物缺失 (`artifacts_missing`)** | Job 状态为 completed，但字幕文件未写入 | `SNOS-411` 历史已完成 | 物理文件大小检查失败，报错 `exit 1` | ExitCode: 1, 未访问旧Job | ✅ 符合预期 |

所有测试用例已正式纳入 `scripts/test_deployment_verification.py`，随 `make test` 持续回归运行。

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
   - Python 单元测试套件全部通过（执行 141 项，跳过 11 项仅限 Docker 镜像环境的测试）。
   - 新增的 8 项部署验证测试全部 PASS。

---

## 五、后续演进确认

按照用户选定并批准的推进路线：
- 当前开源资料整理与部署自检验证已全部闭环。
- **Linux 运行时支持**（同时覆盖 NVIDIA GPU 与纯 CPU 方案）将在此文档任务合并后，由下一个 Builder 任务正式实施。
