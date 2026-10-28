# 三项目统一日志规范

状态：用户已确认。实现由 Herdr 中的 Pi 会话完成，模型为 `magpie / group/flash-models`，thinking 为 `max`。协调会话只负责设计、审查和验收，不修改运行代码。

## 1. 目标和范围

覆盖 `fusionn`、`fusionn-muse`、`fusionn-subs` 的主程序、生产辅助命令、独立 Python 服务、自有脚本、HTTP 框架日志和外部工具输出收集。统一格式，也统一记录什么、何时记录、如何关联和如何脱敏。

主要排障入口是 OrbStack 容器日志页和 `docker compose logs`。应用不新增 `.log` 文件、不双写、不增加应用内轮转器。宿主机独立服务及下载器脚本沿用现有重定向或 `LOG_FILE` 收集入口，并在文档中标明它们不一定出现在容器日志页。

不重写 experiments、开发 mock 工具、测试报告或 CLI 的机器可读结果。不改任务业务身份、队列可靠性、重试决策或翻译结果成功标准。不引入 OpenTelemetry、日志平台、跨仓库公共模块、第三方程序 fork 或新的日志后端。

独立第三方基础设施（如 Redis）只对齐容器日志保留策略，不重写其内部日志格式。

## 2. 库和维护边界

- Go 保留现有 `go.uber.org/zap v1.27.0` 及各仓库 `pkg/logger` 入口。
- Python 使用标准库 `logging`；shell 使用 `printf` 和现有运行环境。
- 不为改日志增加第三方运行时依赖；三个 Go 包以相同的格式规范、黄金样例及一致的基础实现保持同步，不新建公共模块。
- zap 自带 ConsoleEncoder 的上下文字段是 JSON 尾部，不能把它直接当作本规范的平铺字段输出。实现者先提出最小渲染路径，避免大型自定义 Encoder、全套通用日志框架或冷门编码库。
- 先对齐核心输出及黄金用例，再扩展调用点。可以沿用兼容 API，但运行链路的关键事件必须有明确的事件名和关联字段，不能只给旧文案套一个无意义的统一前缀。

## 3. 单行文本格式

```text
2026-07-01T14:20:03.456+08:00 WARN component=translator event=translation_retry job_id=abc error="provider timeout" retry=1 retry_after_ms=2000 msg="Translation will retry"
```

### 基础规则

1. 时间为 `YYYY-MM-DDTHH:mm:ss.SSS+08:00`。在格式化器内转换为固定 UTC+8，不为日志修改全局 `TZ`，不影响业务计时或配额重置。
2. 级别统一为 `DEBUG`、`INFO`、`WARN`、`ERROR`。Python 的 WARNING 映射为 WARN。致命启动错误记录 ERROR，再由程序退出；不能因此改变退出码。
3. 每条应用事件恰好一个物理行。换行、回车、制表符和控制字符必须转义，不能靠多行缩进表达事件结构。
4. 不含 emoji、树形缩进、ANSI 清屏、进度条或纯装饰分隔行。首版不输出颜色，保证终端与收集结果一致。启动时的项目 ASCII banner 例外：每次启动打印一次，并显示版本与实际生效的日志等级；其余业务事件仍遵守单行规则。
5. 消息使用简短英文。文件名、媒体标题、上游错误等值不翻译。事件和字段用 `snake_case`，耗时统一为数值 `duration_ms`，不混用格式化的秒字符串。
6. 文本值统一用双引号及反斜杠转义；数字和布尔值不用引号。只有匹配 `[A-Za-z0-9][A-Za-z0-9_.-]*` 的标识字符串可以裸写；路径、中文、空串和其他字符串均加双引号。`msg`、`error`、`stack`、`preview` 始终带双引号。转义采用 Go 标准库 `strconv.Quote` 语义，Python 以相同黄金向量复核；保留可打印中文，不使用 `QuoteToASCII`。Go 与 Python 对同一黄金输入必须逐字节一致。shell 必须通过共同黄金向量，至少处理 ASCII/C1 控制符、U+2028/U+2029 和矩阵内已知非打印字符；无法按类别识别的其他罕见 Unicode 字符可保留为明确记录的受限范围，不引入新运行时或 Unicode 数据库。不允许一端尾部 JSON、一端 key=value。
7. 必填字段：`component`、`event`、`msg`。输出不包含 `service` 字段：应用/容器/服务身份已由收集入口（容器名、宿主服务单元、进程）提供，行内不再重复；`service` 仍是保留键，调用方不能作为自由字段注入或伪造。独立服务（`word-align`、`host-asr`、`duosubs`）的身份同样由收集入口区分。`component` 指实际模块，不能用调用文件名替代稳定模块名。
8. 固定字段顺序：`component event`，随后为存在的 `job_id submission_id attempt_id request_id parent_job_id stage tool stream`；其余字段按键名排序；`msg` 最后。未知或不适用的关联字段省略，不填空串、null、0 或虚构 ID。
9. 字段名必须匹配 `[a-z][a-z0-9_]*`，不从用户输入生成。拒绝非法键；用户输入不能覆盖 `level`、`service`、`event` 或关联 ID，也不能通过重复保留字段制造歧义。所有不可信文本转义后作为值输出。

### 样例事件

```text
2026-07-01T14:20:01.123+08:00 INFO component=worker event=job_started job_id=abc msg="Job started"
2026-07-01T14:20:02.123+08:00 INFO component=translator event=tool_started job_id=abc tool=llm-subtrans msg="Translation tool started"
2026-07-01T14:20:03.456+08:00 WARN component=translator event=translation_retry job_id=abc error="provider timeout" retry=1 retry_after_ms=2000 msg="Translation will retry"
2026-07-01T14:21:00.789+08:00 ERROR component=worker event=job_failed job_id=abc stage=translation error="retries exhausted" msg="Job failed"
```

事件名应表达已经发生的事实，例如 `service_started`、`config_reloaded`、`job_received`、`job_started`、`stage_completed`、`job_completed`、`job_failed`、`job_linked`、`translation_retry`、`tool_started`、`tool_completed`、`request_completed`。不要求为所有普通诊断消息建大枚举，但关键生命周期事件必须可按 event 检索。

## 4. 级别和错误责任

统一通过 `LOG_LEVEL` 控制，默认 INFO，不因开发环境自动开启内容片段日志。允许 DEBUG/INFO/WARN/ERROR。非法值必须有可见的启动诊断，不能悄悄关闭日志。`ALIGN_LOG_LEVEL` 仅在已有 Python／word-align 配置中作兼容别名，且只在未设置 `LOG_LEVEL` 时使用；Go 服务只读取 `LOG_LEVEL`，不新增历史上没有的别名。文档明确适用范围与优先级。

| 级别 | 内容 |
| --- | --- |
| INFO | 启动/退出、配置生效摘要、任务接收、阶段变化、最终结果和耗时、必要的进度摘要 |
| WARN | 可恢复故障、重试、降级、资源冲突，说明后续处理或等待时间 |
| ERROR | 当前操作最终失败，记录失败阶段、原因、关联标识及必要调用位置 |
| DEBUG | 受控诊断详情、请求阶段细节及经挑选的限长内容片段 |

- 错误沿调用链返回并补充原因；由最终处理该操作失败的边界记录一次 ERROR。下层不能记录同样 ERROR 后再原样返回；HTTP 结束摘要也不能重复输出同一完整错误详情。
- 重试失败与最终失败是不同事件。尚会重试时用 WARN；最终放弃才用 ERROR。
- 正常取消、停机、空队列、缓存 miss 不作为 ERROR。失败影响独立操作时可各有一条错误，但必须区分操作，不能机械全局去重。
- 未预期异常、panic 保留必要的调用位置和异常栈。栈以转义字段表示，脱敏且有上限，不能破坏单行格式。常规业务失败不自动附整份栈。
- HTTP 每个请求一条结束摘要，记录 method、归一化 route、status、request_id、duration_ms。成功 health 检查降 DEBUG。5xx 明确可见；4xx 按原因使用 INFO/WARN，而非一律 ERROR。处理器已有 ERROR 时请求摘要只记状态/耗时，不重复错误正文。
- Gin recovery、uvicorn error/access、Python warnings/第三方 logger 不得旁路标准格式。禁止重复 handler 或 propagate 导致一条记录输出两次。

## 5. 身份与关联

- `job_id` 始终是业务 Job ID，不称为 trace_id，不生成一个替代 ID 覆盖现有身份。
- `request_id` 代表一次请求；合法传入值可沿用，否则生成并在响应中回传。限制为 ASCII 字母数字、点、下划线、短横线，长度 1–64；不得把用户输入原样拼入日志。
- 请求上下文和任务上下文显式传递，并发任务不能通过修改全局 logger 串号。
- Muse 的 Submission 在 Job 之前存在，记录 `submission_id`；产生或解析到 Job 时记录关联事件。执行时记录真正的持久化 `attempt_id`，不要把 `Retries+1` 等序号当成执行 ID；如需重试序号，另用 retry/attempt_number。
- Fusionn -> Redis -> Subs -> callback 已有业务 ID 必须沿用。Fusionn 回调合并若创建新 Job，则保留既有业务行为，并记录 `job_linked`（新 `job_id` 与原 `parent_job_id`）；记录链路两端的关联信息，不重命名或合并现有 Job。
- 独立的 Muse 任务与 Fusionn/ Subs 任务不硬并为同一个 Job。HTTP 下游如 host-asr、word-align 保留请求 ID 和已有任务字段。
- 队列消息或 callback 如确需新增关联字段，只能向后兼容地添加可选字段；先向协调者说明，不做业务协议重构。

## 6. 脚本、工具和敏感数据

### 收集和协议

- 应用服务输出标准流；脚本日志用 stderr，stdout 保留机器结果。尤其 `detect_hard_sub.py` 的 stdout 必须仍是原先布尔值。
- 自有脚本须在第三方 import 前配置 logging；独立服务及其 worker 都要使用相同格式。
- 子进程的自有规范化日志只渲染一次；不得重复时间、级别、服务前缀。运行者先说明可信自有脚本与未知第三方输出的区分方案。
- INFO 显示工具开始、结束、耗时、退出码和必要进度摘要，不逐行转发未知原文。DEBUG 也只记录受控内容，不将第三方 stderr/stdout 当成可无条件公开的日志。
- stderr 不等于 ERROR：ffmpeg、下载器、Python logging 可能将正常进度写到 stderr。
- 失败摘要可附有界且经过筛选的诊断；包含字幕/prompt/body 的未知尾部不能在 INFO 原样回放。保留真实退出码、失败阶段和安全的错误原因。
- 原始结果/错误判定与显示用副本分开。颜色清理、加字段、过滤、截断不得改变 OCR 结果、`detectScriptFailure` 或 `Error:/Traceback` 等原有成功/失败判断。不能靠修改文案让失败消失，也不能把“曾记录任意 ERROR”新增为任务失败条件。成功中出现可恢复 ERROR 的 fixture 必须仍成功；失败条件的超集不是语义等价。
- `bufio.Scanner` 的默认 ScanLines 已会返回末尾无换行的非空行；对此增加回归测试，但不能以不存在的丢行问题为由扩大改造。
- 持续排空 stdout/stderr，处理分块 UTF-8、长行、回车进度条、末尾无换行、提前取消；不能因日志过滤或行长限制导致管道堵塞。缓冲有界，不能保留无上限的工具输出。如果限制原始捕获会改变失败检测，先对原始流完成检测/摘要，再淘汰旧内容。
- 不为 shell 引入 Python/jq 等原本不要求的依赖。优先使用实际宿主环境提供的真实毫秒；如果 `date` 不支持小数秒，允许输出秒级时间（小数位为 `.000`），但每条此类事件必须带 `time_precision=second`，并在启动/首次使用时记录一次可见 WARN。这不是声称提供真实毫秒，也不能每次事件都重复警告。

### 内容限制

- 所有级别都屏蔽 API key、token、密码、Authorization、URL userinfo/凭据 query、命令行密钥及它们出现在错误文字中的形式。优先记录安全字段白名单，不能只依赖一次全局正则。
- INFO 不记录整份配置、字幕、prompt、LLM 原始响应、HTTP 请求/响应 body 或完整命令环境。
- DEBUG 可以在明确的 `preview` 字段内记录经过脱敏的挑选片段，最多 512 个 Unicode 字符；超出时带 `truncated=true`。不能通过分拆成多条 preview 把整份内容重新写入日志。限制按字段名执行，不能靠普通字符串字段或 INFO 调用绕过。
- 未知第三方输出也受上条限制；禁止通过 DEBUG 原样逐行转发绕过原文限制，也不能逐条把 msg 截到 512 字符后在 INFO 输出。第三方库经过 root formatter 包装并不意味着内容可信；在产生处选择安全事件/字段，保留脚本失败判定所需的等价常量标记，不以统一格式的正则匹配替代安全策略。
- 文件路径、媒体标题和参数值必须引用/转义，换行不能伪造下一条日志。可记录必要操作路径，但不得把无关业务正文当作错误原因。

## 7. Docker 收集和保留

应用不新增文件 sink。Compose 中为管理的服务配置：

```yaml
logging:
  driver: json-file
  options:
    max-size: "20m"
    max-file: "6"
```

`max-file` 包含当前文件，因此是当前文件加五份备份，约 120 MiB/容器。按容量淘汰，不保证天数；Docker JSON 包装也占空间。此设置仅对新建/重建后的容器生效。不要为了实施测试重启用户正在运行的服务或 Docker。

宿主机独立服务与下载器完成脚本保留原有收集入口，文档列出怎样查看及是否需要外部轮转。不新增后台采集守护进程。

依据：[Docker json-file driver](https://docs.docker.com/engine/logging/drivers/json-file/)。

## 8. 分仓任务与验收

### Fusionn

- `pkg/logger`、主程序启动和 HTTP middleware/recovery、webhook/callback、Redis 投递、合并队列和所有生产 pipeline 关键事件。
- 子进程收集和独立 `word-align`、DuoSubs Python 服务日志。
- 回调新 Job 与原翻译 Job 的关联。保留 subtitle-merge CLI 的结果输出，统一其诊断输出。
- 更新当前 word-align 日志文档，避免留两份相互冲突的格式说明。

### Fusionn-Muse

- logger 上下文、Submission/Job/真实 Attempt、各处理阶段、错误归属、工具 runner。
- `llm_subtrans_translate.py`、`detect_hard_sub.py` 的诊断、host-asr、host DuoSubs/uvicorn 以及 torrent-complete.sh。
- 保留 OCR stdout、持久化身份、各类恢复/重试含义和已有 HTTP 状态/退出码。

### Fusionn-Subs

- logger、worker/重试/provider fallback、callback、各 translator、相关 glossary/modelselection 诊断。
- 收敛 executeScript/streamDimmed 的重复报错与不受控内容转发，保持成功文件/失败文本识别行为。
- 不修改构建时拉取的第三方 llm-subtrans 源码来实现日志统一；不顺手更改版本策略。

### 必须提交的证据

1. 三仓和 Python 的同一组黄金样例：固定时间、WARN 映射、含空白/中文/等号/引号/换行的值、字段顺序，实际输出单行。
2. INFO/DEBUG 开关和非法 LOG_LEVEL、重复初始化、nil/初始化失败路径、安全退出。
3. 两个并发任务上下文不串号；Submission -> Job、真实 Attempt、Fusionn -> Subs -> callback -> 新合并 Job 可查回。
4. 重试用 WARN、最终 ERROR 一次、health 抑噪、框架异常格式、栈转义。
5. 凭据/URL/命令/错误文本脱敏，片段 Unicode 限长，第三方多行原文不可绕过限制。
6. 自有脚本和未知工具、stdout/stderr、多种分块/长行/回车/无末尾换行/取消的收集测试；OCR stdout 和脚本结果判定回归。
7. Compose 配置可解析、轮转选项在所有目标容器生效；不能实际重启服务来验证。
8. 每仓运行 `make lint` 和 `make test`，提供命令、退出码及可读取的日志路径。失败或依赖缺失要明确上报，不能标成通过。Python 服务测试必须可用 stub，不为单元测试下载大型模型。
9. 提供各仓正常任务、重试、最终失败、工具输出和 HTTP 摘要样例。审查者独立复核，不把作者自报当作最终验收。

## 9. 协作与变更纪律

基线：Fusionn `ec8eecf`，Muse `8d6dd91`，Subs `3549576`。各仓分支 `feat/unified-logging`，使用独立 Herdr worktree，不在原 main 工作目录改运行代码。

实现者先经 intercom 报告核心 API/渲染方案、关键风险和基线测试，再开展调用点改造；Go 核心优先由 Fusionn lane 出样，其他 lane 可先做仓库本地调查/测试，待协调者审核后同步相同核心，避免三套不兼容实现。

中途发现规范冲突/需改变业务接口/需额外库/毫秒环境限制时先上报。完成时发结构化回执，列出 worktree、基线、变更文件、验证命令/退出码、样例、已知风险。不得推送、合并、删除 worktree、接触其他 lane 或重启运行中服务。Review 缺陷由原实现者修复；协调者不代写实现。

### 实施前设计审查补充

- 保留 `Log *zap.SugaredLogger` 的可注入能力；不另建 Field/Str/Int 等重复类型系统。复用 zap 的字段或键值 API，只为 Preview/身份上下文保留必要策略 helper。
- 生产入口保留一次 `version.PrintBanner` 调用，在 banner 中显示 version 与实际生效的日志等级；`service_started` 仍保留 version 字段。
- Muse Python formatter 可集中在 `scripts/logfmt.py`。宿主机入口从 `scripts.logfmt` 导入；脚本入口本地导入；测试覆盖实际入口，Dockerfile 必须包含 helper。不为此修改 `sys.path`。
- Muse -> host-asr 可新增可选的 `attempt_id`，已有 `attempt` 继续原序号含义。
- 替换锁定旧前缀/INFO 内容原文的测试断言获准，但必须换成更强的格式、安全和结果等价断言，不能只删测试。
- Subs 基线 `make lint` 因本机 golangci-lint v2 无法读取既有 v1 配置失败。先保留原始失败证据，可在 `/tmp` 调查兼容配置作为额外检查；未经 review 不修改 tracked lint 配置，不将额外兼容检查冒充原生命令通过。
- `.pi/` 等会话生成产物不属于实现提交。
