# Muse 字幕翻译引擎替代方案

> 本文记录接线前的调查。后续仓库代码已接入 Custom Server：非流式 `/chat/completions`、HTTP timeout=180 秒、PySubtrans 重试一次；已在隔离镜像内用本地模拟服务器验证协议、503 重试和读超时。尚未部署，也未验证真实 CLIProxyAPI/Grok 请求或翻译质量。

## 结论

**首选保留 llm-subtrans，先验证其官方 Custom Server provider，而不是立即换引擎。** 当前问题主要是上游卡顿与客户端重试叠加；换项目不能治愈上游挂起。Custom Server 使用直接 HTTPX、支持请求超时，可以绕过 OpenAI SDK 的隐式重试，同时保留现有字幕翻译流程。[1][2]

现成替代品中，VideoCaptioner 接入成本较低，因为 Muse 已包含其核心和翻译脚本；Cerlancism/chatgpt-subtitle-translator 是值得试验的独立 headless CLI。但目前没有同源字幕、同模型的质量或故障对比测试，不能宣称它们更稳定、日中翻译更好或更快。

范围仅为持久化 Transcription 之后的 Translation，不更换 MLX Qwen3-ASR，不实施迁移。

## 对比

| 方案 | 适合 Muse 的能力 | 主要限制 | 判断 |
| --- | --- | --- | --- |
| llm-subtrans + Custom Server | 官方 Python 库/CLI；兼容自定义 API 地址；场景、上下文摘要、批次和字幕验证；部署版支持显式 timeout [1][2][3] | timeout 是读写等待参数，不是覆盖所有重试的墙钟截止；当前 Muse 未接线；真实 Grok 兼容性尚未测试 [1][2][4] | **第一选择：先验证这个最小改动** |
| VideoCaptioner 翻译核心 | 已在镜像内；OpenAI 兼容接口；固定条数批次、并发、可选反思、结果校验与缓存 [5][6] | 部署快照仍有 SDK 默认超时/重试；运行中的 HTTP 不因取消 Future 而终止；可能容忍部分失败 [5][7][8] | 现成替代品中最方便复用，但不是默认更可靠 |
| Cerlancism/chatgpt-subtitle-translator | Node CLI；兼容 OpenAI API；翻译历史上下文、自适应缩批、普通模式进度恢复 [9][10] | 新增 Node 运行时；SDK maxRetries=3 再叠加外层重试；没有已验证的总截止；结构化输出与 Grok 的兼容性待测 [9][10][11] | 有意义的试验候选，暂不直接迁入生产 |
| Muse 自有窄适配器 | 可围绕既有 SRT、队列与取消边界设计唯一重试预算、严格 cue 验证和批次 checkpoint | **设计选项，尚不存在**；上下文、解析、恢复和测试均由 Muse 维护 | 仅当现有库的适配长期过于困难时考虑 |

以上比较是接口和源码调查，不是翻译质量排名。维护多个模型客户端或字幕工具，不自动改善模型本身的翻译表现。

## 当前集成为何没有用上已有超时能力

部署包为 pysubtrans 1.6.0，Muse 的 `scripts/llm_subtrans_translate.py` 硬编码 `provider="OpenAI"`，只传模型、地址、凭据、目标语言和指令。该分支的 OpenAI 客户端初始化不显式设置 SDK timeout/max_retries，另有 PySubtrans 外层重试。[12]

此前在部署容器中离线核验：SDK read timeout 为 600 秒、SDK 重试为 2 次，PySubtrans 外层重试为 1 次。全部请求读超时的故障注入触发了 6 次 HTTP 尝试，没有访问上游。PySubtrans 的 `max_retries` getter 使用 `value or 3`，因此设置为 0 会回退为 3；切换 provider 不会自动消除这一陷阱。[13]

同一部署版本的官方 Custom Server provider 支持 `timeout`，默认 300 秒，也读取 `CUSTOM_TIMEOUT`。它把该值传给 `httpx.Client`，没有 OpenAI SDK 重试层，仍保留自己的有限重试。[1][2]

本次使用公共 `init_options` / `init_translator` 离线构造 Custom Server：指定无服务的回环地址、`/v1/chat/completions`、模型 grok-4.5、timeout=180、max_retries=1。结果确认为 CustomClient、timeout=180、外层 retries=1。**未发送 HTTP 请求，未验证真实 CLIProxyAPI/Grok 协议兼容或翻译质量。** 这是可评估的最小路径，不是已完成的修复。

HTTPX 的 read timeout 限制等待下一块数据的时间，持续心跳或碎片可能延长总耗时。包含退避、重试、响应读取和退出的总截止需要单独验证；不能把 timeout=180 宣称为整个批次最多运行 180 秒。[4]

## 为什么不直接换回 VideoCaptioner

本地 `Dockerfile` 已包含 VideoCaptioner，`scripts/translate.py` 已调用其 LLMTranslator，并转发 threads、batch_size、提示和反思参数；`internal/executor/translator.go` 是现成适配器。

但 `internal/service/processor/processor.go:57–65` 当前将两个后端成对选择：

- videocaptioner：Whisper + VideoCaptioner 翻译。
- mlx_qwen3_asr：HostASR + llm-subtrans 翻译。

因此不能只把 `pipeline.provider` 改成 videocaptioner，否则 Transcription 也会改变。要保留 MLX，只能独立选择或替换 Translation 入口；当前尚未实施。

部署 VideoCaptioner commit 为 `95842ecb5618c0b6a548a336bdfb0eb859bdb501`。其 SDK 初始化未设置 timeout/max_retries，logging HTTPX client 也未指定超时；离线等价构造仍得到 SDK read=600 秒、retries=2。[5][14] Tenacity 额外的最多 10 次调用只针对 RateLimitError，不是每个 timeout 都额外重试 10 次。[5]

该快照的 `BaseTranslator.stop` 调用 `shutdown(wait=False, cancel_futures=True)`，不能终止已经运行的 HTTP 任务。异常失败的 cue 占比不足 50% 时只告警并返回未翻译数据，达到 50% 才抛错；LLM 格式修正也有回退路径。Muse 若复用它，应增加严格的最终完整性验收，不能只检查输出文件非空。[6][7][8]

成功批次的 diskcache 可以复用，但不是完整的任务 checkpoint。该快照的批次缓存键不包含 custom_prompt、is_reflect 或 endpoint；更换策略时要避免错误复用缓存。[6][7] 部署快照的这些结论不自动适用于未来版本。项目采用 GPL-3.0；许可适配应核对，不据此断言现有集成违规。[15]

## 独立替代品：Cerlancism

Cerlancism/chatgpt-subtitle-translator 的 CLI、翻译历史上下文、自适应缩批和普通模式续译，符合无人值守批量字幕翻译的方向。[9] 普通模式回填源 SRT 的译文；timestamp 模式允许合并条目且不支持相同的进度恢复，不适合作为 Muse 保留原时间轴的默认模式。[9][11]

它支持自定义 OpenAI-compatible baseURL，但当前客户端源码设置 SDK maxRetries=3，另有外层 retry wrapper，所以迁移不能自动消除重试叠加。[10] 默认结构化输出、流式选项、恢复文件与真实 Grok/代理的兼容需要测试；新增 Node/runtime 也比复用已有 Python 引擎增加成本。[9][11] 未进行安装、在线调用、取消延迟或质量实验。

## 建议的下一步

1. 首先比较现有 llm-subtrans 的 OpenAI 与 Custom Server 路径，保留同一源 SRT、模型和提示，不更换 ASR。
2. 做有界故障测试：无首包、持续心跳无正文、断流、错误格式、取消；测量墙钟时间与 Translation lane 释放，而不是只看 timeout 参数。
3. 验证输出 cue 完整、顺序及时间轴不变，并确认重复失败后不会误报成功或重复翻译已验收批次。
4. 若仍有明显适配障碍，再试 VideoCaptioner 或 Cerlancism。翻译质量采用同源样本人工盲评，不以项目 stars、宣传或反思功能推断排名。

这是调查建议，不是已批准的架构变更；本次没有修改生产配置、翻译代码或调用私有服务。

## 第一方来源

1. [llm-subtrans v1.6.0 CustomServerProvider](https://github.com/machinewrapped/llm-subtrans/blob/v1.6.0/PySubtrans/Providers/Provider_Custom.py)
2. [llm-subtrans v1.6.0 CustomClient](https://github.com/machinewrapped/llm-subtrans/blob/v1.6.0/PySubtrans/Providers/Clients/CustomClient.py)
3. [llm-subtrans README](https://github.com/machinewrapped/llm-subtrans/blob/main/readme.md)；[SubtitleValidator](https://github.com/machinewrapped/llm-subtrans/blob/main/PySubtrans/SubtitleValidator.py)（main 能力不自动回溯为部署版）。
4. [HTTPX timeout 官方语义](https://www.python-httpx.org/advanced/timeouts/)
5. [部署 commit 的 VideoCaptioner LLM client](https://github.com/WEIFENG2333/VideoCaptioner/blob/95842ecb5618c0b6a548a336bdfb0eb859bdb501/videocaptioner/core/llm/client.py)
6. [部署 commit 的 VideoCaptioner LLMTranslator](https://github.com/WEIFENG2333/VideoCaptioner/blob/95842ecb5618c0b6a548a336bdfb0eb859bdb501/videocaptioner/core/translate/llm_translator.py)
7. [部署 commit 的 VideoCaptioner BaseTranslator](https://github.com/WEIFENG2333/VideoCaptioner/blob/95842ecb5618c0b6a548a336bdfb0eb859bdb501/videocaptioner/core/translate/base.py)
8. [Python Executor.shutdown 官方语义](https://docs.python.org/3/library/concurrent.futures.html#concurrent.futures.Executor.shutdown)
9. [Cerlancism README](https://github.com/Cerlancism/chatgpt-subtitle-translator/blob/main/README.md)
10. [Cerlancism createOpenAIClient / openaiRetryWrapper](https://github.com/Cerlancism/chatgpt-subtitle-translator/blob/main/src/openai.mjs)
11. [Cerlancism CLI 恢复和 SRT 写入](https://github.com/Cerlancism/chatgpt-subtitle-translator/blob/main/cli/translator.mjs)
12. [llm-subtrans v1.6.0 OpenAIClient](https://github.com/machinewrapped/llm-subtrans/blob/v1.6.0/PySubtrans/Providers/Clients/OpenAIClient.py)
13. [llm-subtrans v1.6.0 TranslationClient](https://github.com/machinewrapped/llm-subtrans/blob/v1.6.0/PySubtrans/TranslationClient.py)
14. [部署 commit 的 VideoCaptioner logging HTTPX client](https://github.com/WEIFENG2333/VideoCaptioner/blob/95842ecb5618c0b6a548a336bdfb0eb859bdb501/videocaptioner/core/llm/request_logger.py)
15. [VideoCaptioner LICENSE](https://github.com/WEIFENG2333/VideoCaptioner/blob/master/LICENSE)
