# 部署检查命令

教程、agent 与测试都使用 `scripts/verify_deployment.py`，无需安装额外 Python 包。请使用 Python 3.10+，从本仓库根目录执行，并保持回调脚本 `scripts/torrent_complete.py` 与检查命令为同一版本。

命令只核对 HTTP、投递记录和产物。**退出 0 不代表本次实际调用了 ASR/LLM，也不代表下游已经刮削或入库。** 当前 Muse API 允许复用转录和翻译结果，不能提供本次模型调用的完整凭证。

## 选择检查目标

| 命令 / 目标 | 做什么 | 副作用 |
| --- | --- | --- |
| `health` | 检查 Muse HTTP 与版本；可选检查 HostASR HTTP | 只读 |
| `run --expect accepted` | 通过真实 qBittorrent 回调脚本投递一次，确认本次 Submission 已入库 | 会启动后台处理，可能消耗模型资源 |
| `run --expect delivered` | 等待关联的初次执行完成，核对实际视频和记录中的最终字幕文件 | 同上 |
| `run --expect subtitles` | delivered 的基础上要求非放行、非 dry-run，核对源/目标 SRT 文件与基本结构 | 同上 |
| `observe --submission-id … --expect …` | 沿已有回执执行相同检查，不重新投递 | 只读 |

`accepted` 即使观察到准备失败也可以通过：它只证明入库，结果会附实际 `preparation_status`。已有中文字幕的样本使用 `delivered`；对它要求 `subtitles` 会得到 `sample_bypassed`，表示样本不适用。

## 使用方法

只读检查：

```bash
python3 scripts/verify_deployment.py health --url http://127.0.0.1:4444
```

macOS 可增加 `--asr-url http://127.0.0.1:8766`。这只证明检查命令所在机器能访问 ASR HTTP，不证明 Muse 容器到 ASR 的访问、模型加载或推理。

投递测试需要用户授权的独立部署。为该部署准备新的数据库、工作目录、字幕和翻译缓存目录，先不接入会搬走文件的下游消费者；不要清理或重启现网来制造测试环境。准备一个有效有声视频，名称能通过 Muse 的媒体识别，文件至少满足当前 `200 * 1024 * 1024` 字节的过滤门槛。空文件、填充字节或测试 fixture 都不能用于真实模型验收。

以下参数须换成实际独立部署的信息。`--host-root` 是本地可读的挂载根目录，映射为 Muse 使用的 `--container-root`；当前容器标准目录为 `/data`。该参数不会修改服务端配置。

```bash
export MUSE_URL='http://127.0.0.1:4444'
export MEDIA_ROOT='/path/to/isolated-media'
export SAMPLE_VIDEO="${MEDIA_ROOT}/torrents/whisparr/VERIFY-123.mp4"
export SAMPLE_NAME='VERIFY-123'
export VERIFY_TIMEOUT='14400'

python3 scripts/verify_deployment.py run \
  --url "${MUSE_URL}" --host-root "${MEDIA_ROOT}" --container-root /data \
  --sample "${SAMPLE_VIDEO}" --name "${SAMPLE_NAME}" --category whisparr \
  --expect subtitles --timeout "${VERIFY_TIMEOUT}"
```

四小时只是示例，不是 CPU/GPU 的性能承诺。`--timeout` 为整个检查的等待预算；处理目标必须显式指定。`--request-timeout` 默认 15 秒，`--poll-interval` 默认 2 秒。单次网络读取受请求超时限制，截止后不再发下一次请求；网络调用退出可能略晚于总预算。命令超时不会取消 Muse 中仍在运行的任务。

stdout 恰好输出一个 JSON 结果，stderr 输出简短提示。可用 `> /path/to/private-result.json` 保存 stdout。结果含媒体路径，公开分享前仍应脱敏。命令不会输出配置快照、远端错误正文、字幕文本或模型凭据。

继续观察已有回执：

```bash
python3 scripts/verify_deployment.py observe \
  --url "${MUSE_URL}" --submission-id '<返回的 submission_id>' \
  --host-root "${MEDIA_ROOT}" --container-root /data \
  --expect subtitles --timeout "${VERIFY_TIMEOUT}"
```

单独观察入库可省略路径映射和超时：`observe --url … --submission-id … --expect accepted`。检查远程部署的文件时，必须在可读取对应挂载根目录的位置运行命令；只有 HTTP 访问权限不能证明物理产物存在。

## 结果解释

结果带 `schema_version: 1`、`expectation`、`outcome`、`reason`，以及已获得的关联 ID、当前状态、版本、起止时间和产物信息。以退出码和 JSON 字段为准，不解析 stderr 中文提示。

| 退出码 | outcome | 意义 |
| --- | --- | --- |
| 0 | `pass` | 所选目标证据齐全 |
| 1 | `fail` | 已观察到目标失败，例如准备失败、执行中断或产物缺失 |
| 2 | `inconclusive` | 无法完成证明，例如超时、协议不兼容、旧任务或不适用样本 |

`claims` 分开记录 `submission_persisted`、`delivery_observed`、`subtitle_artifacts_observed`。**`asr_invoked_this_run` 和 `llm_invoked_this_run` 恒为 `not_proven`**，即使字幕核对通过也不改变。

| 常见 reason | 解释与下一步 |
| --- | --- |
| `target_verified` | 所选目标已核对；真实模型和下游还需另验 |
| `category_skipped` | 回调忽略此分类，未投递；确认使用 `whisparr` |
| `submission_outcome_unknown` | POST 超时或回执无效，可能已入库；不要自动重投，按结果中的 request ID 查服务日志，取得回执后 observe |
| `invalid_hook_receipt` | 回调版本不匹配、stdout 被污染或意外中止；检查脚本版本并先查是否已入库 |
| `preparation_failed / ignored / interrupted` | 本次准备未成功，检查媒体和服务状态；不要转查同名旧任务 |
| `execution_failed / interrupted` | 关联执行未成功；不会自动恢复或重试 |
| `existing_job / ambiguous_history / non_initial_execution` | 检测到旧 Job、多次准备或非初次执行，不能证明本次样本处理；使用新的隔离环境，不删除现网记录 |
| `execution_changed / artifacts_changed` | 观察期间出现执行或路径变化；本次证据不稳定，检查任务历史 |
| `sample_bypassed` | 已有字幕直接放行；若只验交付，改用 delivered；验转录需无中文字幕的样本 |
| `dry_run / settings_unavailable` | 测试模式或缺少目标执行的配置证据，不能证明字幕处理 |
| `artifact_missing / artifact_empty_or_not_file / invalid_srt` | 实际动态路径的文件缺失、为空或字幕格式无效；下游已移走视频时也无法完成当前文件核对 |
| `unmapped_artifact / artifact_unreadable` | 路径映射、目录或权限不允许读取；不会降级为通过 |
| `timeout / transport_error / http_error` | 等待超时或接口无法读取；查服务状态后可用同一回执继续观察 |
| `incompatible_response / identity_mismatch / history_unavailable` | 必要字段、身份或历史不满足契约；核对服务版本，不能默认成功 |
| `invalid_arguments` | 参数缺失或无效，使用 `--help` / 子命令 `--help` 查看用法 |

首版仅接受能消歧的 initial Attempt；自动重试、人工恢复或重翻译不会被拼接成同一次成功。重复提交不会启动新的处理。源/目标字幕做 UTF-8 SRT 基本结构与摘要检查；视频只做路径、普通文件、可读和非空检查，不能认证编码或内容质量。

为避免失控读取，HTTP 响应最多 4 MiB、单份字幕最多 32 MiB、历史最多 100 页；超限返回证据不足。Basis：own judgment，限定命令资源使用，不是媒体业务限制。路径和状态回读只能描述检查时观察到的事实，不能锁定未来文件版本。

## 回调 JSON 协议

qBittorrent 的原四个位置参数不变，默认 stdout 仍为空。检查命令启用可选首参数 `--receipt-json`，不从日志猜测编号：

```bash
python3 scripts/torrent_complete.py --receipt-json \
  '<name>' whisparr '<info-hash>' '<host-media-path>'
```

回执含 `schema_version: 1`、`result`（`accepted / skipped / rejected / unknown`）、`reason`、`request_id`，有 HTTP 回应时含 `http_status`，确认成功时含 `submission_id`。分类跳过保留原退出 0，但 result 为 skipped。POST 恰好一次，不跟随重定向、不自动重发。

可用 `--hook /path/to/torrent_complete.py` 检查独立部署的同版本脚本，输出会记录脚本摘要。检查命令通过 `MUSE_URL`、`HOST_INPUT_PATH`、`CONTAINER_INPUT_PATH` 传映射，通过 `MUSE_REQUEST_ID` 与 `MUSE_REQUEST_TIMEOUT` 传本次关联和超时。不要用旧版回调的日志代替 JSON 协议。

## 真实部署还要验证什么

1. 同一独立部署运行真实有效视频，用共用命令保存关联 ID 和产物证据。记录镜像版本、配置摘要、实际模型、OS 与硬件，不公开凭据。
2. 核对该 Job/Attempt 的 ASR 请求及实际推理完成证据，以及 LLM 实际请求成功的证据。只看到处理阶段完成、子进程启动或旧字幕存在都不充分；无法关联时写“未证明”。
3. 抽样查看视频、源字幕、翻译字幕，确认时间轴、语言方向及内容。结构检查不能判断翻译质量。
4. macOS Apple Silicon、Linux NVIDIA、Linux 纯 CPU 分别验收。NVIDIA 应有实际设备推理证据；CPU 或 macOS 成功不能替代它。本轮实现测试没有完成这些部署验收。
5. 真实 qBittorrent 完成事件产生回执后，用 observe 跟踪；直接调用脚本不能证明 qBittorrent GUI 配置正确。再对同一媒体验证 mdc-ng 整理、115strm `.strm` 生成和 Emby 入库/播放，分别留证。

自动化测试中的模拟服务和替身产物只证明命令行为与 API 契约；它们不是以上真实部署证据。技术设计、来源和本轮实现检查记录见[部署验证设计](../design/deployment-verification.md)。
