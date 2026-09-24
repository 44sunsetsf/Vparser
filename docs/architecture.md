# Vparser 架构与服务契约

本文档是 `server-go`（网关与调度）和 `agent-py`（Agent 推理）两个服务共同遵守的契约，接口以 `proto/agent/v1/agent.proto` 为准。任何跨服务的格式改动，都必须同时改动本文件和 proto。

## 1. 服务划分

```
浏览器 ──HTTP/SSE──► web (nginx) ──► server-go :9090 ──gRPC──► agent-py :9091
                                        │  Kafka 生产/消费
                                        │  Redis：锁、限流、幂等、进度、事件
                                        │  MinIO：视频与关键帧
                                        │  FFmpeg / ASR / Tesseract：构建 VideoContext
                                        ▼
               MySQL · Redis · Kafka · MinIO · Qdrant · Jaeger · Prometheus
```

| 服务 | 职责 |
|---|---|
| `server-go` | 所有对外 HTTP 接口（鉴权、上传、秒传、媒体管理、分析提交与状态、SSE、管理后台）；限流与幂等；Kafka 投递与消费；分布式锁；VideoContext 构建（音频切片 ASR、关键帧抽取、帧去重、OCR）；失败台账；把领域对象转成前端 JSON |
| `agent-py` | Planner → Executor → Critic 工作流、全部 prompt 与模型调用、意图路由、长视频分块与混合检索、证据校验、执行预算、遥测、轻量追问，以及 Agent 侧 Checkpoint（plan / criticState / result / chunks / revision）和用户反馈 |

`agent-py` 只在内网监听 gRPC，不对外暴露端口。

## 2. gRPC 约定

- **鉴权**：每次调用都带 metadata `x-internal-token: $INTERNAL_TOKEN`，服务端拦截器校验，不匹配返回 `UNAUTHENTICATED`。健康检查（`grpc.health.v1.Health`）不需要 token。
- **Deadline**：客户端每次调用都必须设置 deadline。服务端的 Agent 执行预算取 `min(AGENT_MAX_DURATION_MS, 剩余 deadline − 2s)`，保证服务端一定先于客户端放弃，不做无用功。
  - `Run`：`AGENT_RUN_TIMEOUT_MS`，默认为预算 + 60 秒；
  - `FollowUp` / `ClassifyMode` / `SearchEvidence`：`AI_INTERACTIVE_TIMEOUT_MS`；
  - 其余调用：10 秒。
- **错误码**：

| 状态码 | 含义 | 网关（消费者）的处理 |
|---|---|---|
| `INVALID_ARGUMENT` | 请求非法，或重试也不会成功的失败 | 永久失败：写失败台账，投递 DLQ，提交 offset |
| `FAILED_PRECONDITION` | 视频上下文尚未构建 | 对外返回 409 |
| `RESOURCE_EXHAUSTED` | 执行预算（时长 / Token / 成本）耗尽 | 标记 `BUDGET_EXHAUSTED`，不重试 |
| `UNAVAILABLE` / `DEADLINE_EXCEEDED` / `INTERNAL` | 临时故障 | 进入分级重试 |
| `UNAUTHENTICATED` | 内部 token 配置错误 | 当作临时故障处理，同时打错误日志 |

- **过载与预算的区分**：`agent-py` 的并发 RPC 数达到 `AGENT_GRPC_MAX_CONCURRENT_RPCS` 时，gRPC 运行时直接返回 `RESOURCE_EXHAUSTED`（不排队）。真正的预算耗尽会额外带 trailing metadata `x-agent-error: BUDGET_EXHAUSTED`；网关只在有该 trailer 时按预算耗尽处理，没有 trailer 的 `RESOURCE_EXHAUSTED` 视为过载，按临时故障重试。
- **预算被 deadline 截断**：如果执行预算是被调用方 deadline 截短的（而不是 `AGENT_MAX_DURATION_MS`），时长耗尽时返回 `DEADLINE_EXCEEDED`（可重试），而不是 `RESOURCE_EXHAUSTED`。剩余 deadline ≤ 2s 的请求直接返回 `DEADLINE_EXCEEDED`，不开始执行。
- **轻量追问**：`FollowUp` 只做检索 + 一次模型调用，回答为 Markdown，引用时间戳写成纯文本 `[mm:ss]` / `[h:mm:ss]`（由前端转成跳转链接）。服务端会校验每个时间戳都落在本次检索到的片段内，不在其中的会去掉方括号并标注“未核验”。

- **只读调用自动重试**：`GetResult`、`GetPlan`、`GetTrace`、`GetEvaluation`、`ListFeedback`、`SearchEvidence` 通过客户端 service config，在 `UNAVAILABLE` 时最多重试 3 次，指数退避。写操作和 `Run` 不在 RPC 层重试，统一交给 Kafka 重试链路。
- **JSON 输出**：网关把 proto 转成前端 JSON 时，字段名使用 camelCase（proto 的 `json_name`），int64 输出为数字，枚举输出为名称字符串（`GENERAL` 等），可选字段缺省时输出 `null`。

## 3. Kafka

| Topic | 分区 | 用途 |
|---|---|---|
| `video.analysis` | 6 | 分析任务。key = contentHash，同一内容的任务落在同一分区，天然减少跨实例的锁竞争 |
| `video.analysis.retry.10s` | 3 | 第 1 次重试，延迟 10 秒 |
| `video.analysis.retry.60s` | 3 | 第 2 次重试，延迟 60 秒 |
| `video.analysis.dlq` | 1 | 死信：重试耗尽、永久失败、毒消息 |

- **生产者**：幂等生产者（`enable.idempotence`），`acks=all`。提交接口同步等待 broker 确认后才返回 202。
- **消费者组**：`video-analysis-worker` 同时订阅主 topic 和两个重试 topic。关闭自动提交，**业务处理完成（成功、进入重试 topic 或进入 DLQ）之后才提交 offset**，语义为 at-least-once，重复由业务层的幂等兜住。
- **消息体**：JSON `{"mediaId","action":"START_ANALYSIS|REVISE_ANALYSIS","contentHash","userGoal","mode"}`。
- **Headers**：`x-attempt`（从 1 开始）、`x-not-before`（Unix 毫秒，重试 topic 的最早处理时间）、`x-first-error`，以及链路追踪的 `traceparent`。死信记录额外带 `x-dlq-reason`（`permanent` / `exhausted` / `poison`）。
- **重试**：
  - 可重试失败：第 1 次失败投 `retry.10s`，第 2 次投 `retry.60s`，第 3 次失败投 `dlq` 并写失败台账。
  - 重试 topic 的消费者读到 `x-not-before` 还没到的消息时，暂停该分区（`PauseFetchPartitions`），到时间后恢复；在此之前不提交 offset。
- **毒消息**：无法解析或缺字段的消息，只要写失败台账和投 DLQ 两者有一个成功，就提交 offset；两者都失败则不提交，下次重新消费。
- 服务启动时通过 admin 客户端幂等地创建上述 topic。

## 4. Redis Key

| Key | 类型 / TTL | 用途 |
|---|---|---|
| `auth:session:{base64url(sha256(token))}` | String / 24h | 登录会话（只存 token 的哈希，Redis 泄露也拿不到可用 token） |
| `auth:login-failures:{username}` | String / 10min | 登录失败计数（8 次锁定） |
| `analysis:active:{contentHash}:{goalDigest}` | String / 6h | 提交幂等：正在处理 |
| `analysis:completed:{contentHash}:{goalDigest}` | String / 7d | 已完成结果的归属 mediaId（跨用户复用） |
| `lock:analysis:{contentHash}:{goalDigest}` | 锁 / 30s + 看门狗 | 消费互斥 |
| `lock:analysis-context:{contentHash}` | 锁 | 同一内容只构建一次 VideoContext |
| `analysis:context-owner:{contentHash}` | String / 7d | 已构建上下文的归属 mediaId |
| `lock:upload:merge:{uploadId}` | 锁 | 分片合并互斥 |
| `upload:chunked:{uploadId}`、`upload:chunked:{uploadId}:parts`、`upload:chunked:{uploadId}:completed` | Hash / Set / String，24h | 分片上传进度 |
| `upload:challenge:{id}` | Hash / 2min | 秒传校验挑战 |
| `lock:media-object:{contentHash}` | 锁 | 秒传校验+落库 与 删除时的引用计数 互斥（见第 9 节） |
| `media:md5:{mediaId}`、`media:list:v2:user:{userId}` | String / 永久、30min | contentHash 缓存、用户视频列表缓存 |
| `transcription:active:{mediaId}`、`transcription:state:{mediaId}` | String / 2h、最长 7d | 文字提取任务状态 |
| `ratelimit:{scope}` | Hash `{tokens, ts}` | 令牌桶（见第 5 节） |
| `agent:checkpoint:*`、`agent:feedback:{mediaId}` | 见第 6 节 | Agent Checkpoint 缓存、用户反馈 |
| `dovideo:task-events` | Pub/Sub 频道 | 任务阶段事件 |

**goalDigest**（两个服务共享的规则）：`GENERAL` 模式为 `sha256_hex(trim(goal))`；其它模式为 `sha256_hex(MODE + "␟" + trim(goal))`。这里的 `trim` 只去掉首尾码点 ≤ U+0020 的字符，全角空格等 Unicode 空白会保留。两个服务必须使用同一实现，测试向量见两侧的单测。

**contentHash**：MD5 小写 hex。不是合法 MD5 时，退化为 `media-{mediaId}`。

## 5. 限流（令牌桶）

- 实现：一段 Lua 脚本，状态存在 Hash `{tokens, ts}` 中。每次请求先按经过的时间补充令牌（不超过容量），再尝试扣 1 个；Key 的 TTL 等于“把桶补满所需的时间”。
- 规则：先扣用户桶，再扣全局桶；全局桶扣失败时，不退还已扣的用户令牌。

| 作用域 | 容量 | 补充速率 |
|---|---|---|
| `ratelimit:analysis:user:{id}` | 5 | 5 个 / 分钟 |
| `ratelimit:analysis:global` | 30 | 30 个 / 分钟 |
| `ratelimit:route:user:{id}` | 10 | 10 个 / 分钟 |
| `ratelimit:route:global` | 60 | 60 个 / 分钟 |

- 被限流时返回 HTTP 429，业务码 42900。

## 6. Agent Checkpoint

- **真源**：MySQL 表 `agent_checkpoints(media_id, checkpoint_key, stage, payload, updated_at)`，主键 `(media_id, checkpoint_key)`，写入用 `INSERT … ON DUPLICATE KEY UPDATE`。
- **缓存**：Redis Hash，TTL 7 天。读的时候先读 Redis，未命中再读 DB 并回填；写的时候先写 DB，成功后再写缓存，缓存失败只打告警。
- **媒体级**：Redis key `agent:checkpoint:{mediaId}`；MySQL `checkpoint_key` 为 `media:context`、`media:chunks`、`media:stage`。
- **目标级**：Redis key `agent:checkpoint:{mediaId}:goal:{digest}`；`checkpoint_key` 为 `goal:{digest}:plan|criticState|result|stage`。
- **修订**：Redis key `…:goal:{digest}:revision`；`checkpoint_key` 为 `revision:{digest}`，payload 为 `{"plan": AgentPlan|null, "applied": bool}`。
- **索引与反馈**：目标索引 Set `agent:checkpoint:{mediaId}:goals`（TTL 7 天）；反馈 List `agent:feedback:{mediaId}`（保留最近 200 条，TTL 30 天）。
- **写入归属**：
  - `server-go` 写 `media:context`（payload 中 `userGoal` 置空）、各级 `stage` 和失败信息（Hash 字段 `failedStage`、`errorType`）；
  - `agent-py` 写其余所有 checkpoint；
  - 两者在时间上串行：网关先落盘上下文，再调用 `Run`。

## 7. 任务事件（SSE）

- **频道消息格式**：发布到 `dovideo:task-events`，消息体为 `{"key":"<type>:<mediaId>:<suffix>","event":{"state","result","message","stage"}}`。
  - `type=analysis` 时，`suffix` 为 goalDigest；
  - `type=transcription` 时，`suffix` 为 `default`。
- **发布方**：两个服务都可以发布。`server-go` 发布 `QUEUED`、`CONSUMING`、`VIDEO_CONTEXT`、`AGENT_LOOP`、`RETRYING`、`COMPLETED*`、`DEAD_LETTERED`、`BUDGET_EXHAUSTED`；`agent-py` 发布 `PLAN_COMPLETED`、`EXECUTOR_*`、`CRITIC_*`、`EVIDENCE_REFRESHED`、`ANALYSIS_COMPLETED*`。
- **订阅**：SSE 连接只在 `server-go`。每个实例都订阅该频道，只推送给本机上订阅了对应 key 的连接。新订阅建立后，先推送一次当前状态。

## 8. 可观测性

- **链路追踪**：OpenTelemetry，OTLP gRPC 导出到 `OTEL_EXPORTER_OTLP_ENDPOINT`（compose 中为 `jaeger:4317`），变量为空时关闭。
  - 传播路径：HTTP（gin）→ Kafka headers（`traceparent`）→ gRPC metadata → agent-py，一次分析是一条完整的 trace。
  - 各服务的 span：Redis、MySQL、MinIO 调用；agent-py 为 Planner、Executor、Critic、每次模型调用、检索各建一个 span，属性中带预估 Token 数和轮次。
- **指标**：Prometheus 格式。`server-go` 暴露在 `:9090/metrics`，`agent-py` 暴露在 `:9464/metrics`。

| 指标 | 类型 | 服务 |
|---|---|---|
| `http_request_duration_seconds{route,method,status}` | histogram | server-go |
| `analysis_submit_total{result}`（accepted / duplicate / rate_limited / error） | counter | server-go |
| `kafka_consume_duration_seconds{topic,outcome}` | histogram | server-go |
| `kafka_retry_total{topic}`、`kafka_dlq_total{reason}` | counter | server-go |
| `redis_lock_acquire_total{name,result}` | counter | server-go |
| `video_context_build_seconds` | histogram | server-go |
| `agent_grpc_client_duration_seconds{method,code}` | histogram | server-go |
| `agent_run_duration_seconds{mode,outcome}` | histogram | agent-py |
| `llm_call_duration_seconds{stage,outcome}` | histogram | agent-py |
| `llm_tokens_total{stage,direction}` | counter | agent-py |
| `agent_critic_rounds_total{passed}` | counter | agent-py |
| `agent_budget_terminations_total{reason}` | counter | agent-py |
| `retrieval_vector_fallback_total` | counter | agent-py |

agent-py 的标签取值：`agent_run_duration_seconds.outcome` 为 `ok` 或小写的 gRPC 状态码名（如 `resource_exhausted`）；`llm_call_duration_seconds.outcome` 为 `ok` / `error` / `timeout` / `deadline`（每次尝试记一次）；`stage` 为 `PLANNER`、`EXECUTOR`、`CRITIC`、`FOLLOW_UP`、`MODE_ROUTER` 等；`direction` 为 `input` / `output`（估算值）；`passed` 为 `true` / `false`；`reason` 为 `duration` / `tokens` / `cost`。

## 9. 秒传

1. 前端在 Web Worker 中增量计算整文件 MD5（2MB 一块，不阻塞 UI）。
2. `POST /media/instant-upload/challenge`，请求体 `{md5, size, filename}`。
   - 库中不存在同 MD5 且大小相同的对象时，返回 `data: null`，前端改走分片上传；
   - 存在时，返回 `{challengeId, offset, length}`：服务端随机选一个 ≤ 1MB 的字节区间。
3. 前端计算该区间的 MD5，调用 `POST /media/instant-upload`，请求体 `{challengeId, rangeMd5}`。
   - 服务端用 Range 请求读取对象的同一区间，校验一致后，为当前用户新建一条 `media_files` 记录，指向同一个对象，返回 `MediaSummary`；
   - 挑战一次性有效，2 分钟过期。
   - 这样可以防止只凭泄露的 MD5 就把别人的视频“秒传”到自己名下。
4. 删除视频时，只有在没有其它 `media_files` 记录引用同一 `file_path` 时，才会删除 MinIO 对象（引用计数）。计数与秒传的“校验 + 落库”都在 `lock:media-object:{contentHash}` 下进行，避免秒传刚校验完对象就被并发删除；拿不到锁或计数失败时保留对象（宁可多占存储，也不误删别人的视频）。

## 10. 配置（环境变量）

| 变量 | 默认值 | 使用方 |
|---|---|---|
| `DB_HOST` / `DB_PORT` / `DB_NAME` / `DB_USERNAME` / `DB_PASSWORD` / `DB_TIMEZONE` | localhost / 3307 / media_db / – / – / Asia/Shanghai | 两者 |
| `REDIS_HOST` / `REDIS_PORT` / `REDIS_PASSWORD` / `REDIS_DATABASE` | | 两者 |
| `KAFKA_BROKERS` | `localhost:9094` | server-go |
| `AGENT_GRPC_ADDR` | `127.0.0.1:9091` | server-go |
| `AGENT_GRPC_PORT` | `9091` | agent-py |
| `AGENT_METRICS_PORT` | `9464` | agent-py |
| `AGENT_GRPC_MAX_WORKERS` / `AGENT_GRPC_MAX_CONCURRENT_RPCS` / `AGENT_GRPC_SHUTDOWN_GRACE_SECONDS` | 16 / 0（= 工作线程数）/ 30 | agent-py |
| `INTERNAL_TOKEN` | 必填 | 两者 |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | 空 = 关闭追踪 | 两者 |
| `OTEL_SERVICE_NAME` | `server-go` / `agent-py` | 两者 |
| `MINIO_ENDPOINT` / `MINIO_PUBLIC_ENDPOINT` / `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` / `MINIO_BUCKET` | | server-go |
| `SILICONFLOW_API_KEY` / `SILICONFLOW_BASE_URL` / `LLM_MODEL` / `LLM_ENABLE_THINKING` / `LLM_TIMEOUT_SECONDS` / `LLM_*_PRICE_PER_MILLION` / `EMBEDDING_MODEL` | | agent-py（ASR 使用同一 key，在 server-go） |
| `AGENT_MAX_ROUNDS` / `AGENT_MAX_DURATION_MS` / `AGENT_MAX_ESTIMATED_TOKENS` / `AGENT_MAX_ESTIMATED_COST` / `AGENT_EVALUATION_ENABLED` | | agent-py |
| `AGENT_RUN_TIMEOUT_MS` / `AI_INTERACTIVE_TIMEOUT_MS` | `AGENT_MAX_DURATION_MS`（默认 120000）+ 60000 / 180000 | server-go |
| `QDRANT_*` | | agent-py |
| `ASR_URL` / `ASR_MODEL` / `YTDLP_PATH` / `FFMPEG_DIR` / `OCR_COMMAND` / `CORS_ALLOWED_ORIGINS` / `SERVER_PORT` / `SERVER_ADDRESS` | | server-go |
