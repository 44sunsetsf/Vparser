<p align="right"><a href="README.zh-CN.md"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/images/lang-dark.svg"><img alt="EN | 中文 — switch to Chinese" src="docs/images/lang-light.svg" width="112"></picture></a></p>

<div align="center">
  <h2>Vparser</h2>
  <p>
    <img src="https://img.shields.io/badge/Go-1.25-00ADD8?style=flat-square" alt="Go 1.25">
    <img src="https://img.shields.io/badge/Python-3.12-3776AB?style=flat-square" alt="Python 3.12">
    <img src="https://img.shields.io/badge/gRPC-Protobuf-244C5A?style=flat-square" alt="gRPC">
    <img src="https://img.shields.io/badge/Kafka-KRaft-231F20?style=flat-square" alt="Kafka">
    <img src="https://img.shields.io/badge/OpenTelemetry-Jaeger-5E6AD2?style=flat-square" alt="OpenTelemetry">
    <img src="https://img.shields.io/badge/Vue-3-42B883?style=flat-square" alt="Vue 3">
    <a href="./LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue?style=flat-square" alt="MIT License"></a>
  </p>
  <p>A <strong>video agent</strong> for long-form content: it turns hours of lectures and talks into structured notes you can search, trace back and keep asking questions about.</p>
</div>

## What it does

Upload a video (a local file or an online link) and describe what you want from it — revision notes, a critique of the arguments, an editing script. The agent reads both the speech and the on-screen text and returns a structured result in which **every claim carries a timestamp**; click it to jump back to the original frame and check. You can keep asking follow-up questions afterwards.

## Screenshots

**Workspace**: upload a local video or import a link (instant upload for known files, resumable uploads) and manage your videos.

![Workspace](docs/images/workspace.jpg)

**Analysis result**: the original video and the structured findings side by side; click a timestamp to jump to that moment.

![Analysis result](docs/images/agent-result.jpg)

**Timestamped evidence**: every finding is bound to verifiable ASR / OCR source text; results that fail the Critic check are clearly flagged.

![Timestamped evidence](docs/images/agent-evidence.jpg)

**End-to-end tracing**: one analysis is a single trace spanning server-go and agent-py (155 spans in this example), showing the time spent in the Planner, the Executor and every model call.

![Jaeger trace](docs/images/trace-agent.jpg)

## Architecture

```
browser ──HTTP/SSE──► nginx ──► server-go (Go) ──gRPC──► agent-py (Python)
                                 │  auth · instant/chunked upload · rate limiting · idempotency
                                 │  Kafka produce & consume (tiered retry + DLQ)
                                 │  distributed locks · VideoContext building (FFmpeg / ASR / OCR)
                                 ▼
          MySQL · Redis · Kafka · MinIO · Qdrant · Jaeger · Prometheus
```

- **server-go**: every public API; handles ingestion, scheduling and the I/O-heavy multimodal preprocessing.
- **agent-py**: the controlled Planner → Executor → Critic workflow, retrieval, evidence checking, budgets and checkpoints; exposed over gRPC on the internal network only.
- The contract between the services lives in [`proto/agent/v1/agent.proto`](proto/agent/v1/agent.proto) and [`docs/architecture.md`](docs/architecture.md).

## Core design

**A reliable task pipeline**
- Instant upload: a Web Worker hashes the whole file with MD5, then the server challenges the client for a random byte range, so knowing someone else's MD5 is not enough to "instantly upload" their video.
- Chunked, resumable upload: 5 MB chunks, progress tracked in a Redis set, a lock held while merging.
- Kafka for async work: idempotent producer with `acks=all`; partitioned by content hash; manual offset commits; 10 s / 60 s retry topics, then a DLQ.
- A home-grown Redis distributed lock: `SET NX PX` + watchdog renewal + compare-and-delete in Lua + lock-loss detection.
- Layered deduplication: idempotency keys on submission, a mutex on consumption, result reuse for the same content and goal, context reuse for the same content.
- Redis Lua token bucket: per-user and global rate limits.

**Temporal multimodal VideoContext**
- Audio is cut into 60-second slices for ASR; keyframes are taken on scene changes plus a 30-second fallback, deduplicated by difference hash, then run through OCR.
- Both paths run in parallel on goroutines with bounded worker pools and are merged into one timeline of segments.

**A controlled agent**
- Planner → Executor → Critic for at most two rounds, after which code verifies every piece of evidence: does the timestamp fall inside a segment, and can the quoted text be found in the ASR/OCR output?
- Three budgets — duration, tokens and cost; the gRPC deadline is propagated all the way into the agent to bound its run time.
- Long videos are split into 5-minute chunks with summaries, keywords and bge-m3 embeddings; retrieval blends semantic, keyword and on-screen-text signals; falls back gracefully when Qdrant is unavailable.
- Stage-level checkpoints (MySQL as source of truth, Redis as cache) let a failed run resume from the last completed stage.
- Follow-up questions take a lightweight path: retrieval plus a single grounded answer, with the cited timestamps verified.

**Observability**
- OpenTelemetry tracing end to end: HTTP → Kafka → gRPC → model calls; one analysis is one complete trace in Jaeger.
- Prometheus metrics: API latency, submission outcomes, consumer time, retries / DLQ, lock contention, model latency and token usage, Critic pass rate, budget terminations.
- The frontend receives task stages in real time over SSE + Redis Pub/Sub, which works across multiple instances.

## Quick start

All you need is Docker Desktop, installed and running:

```bash
./scripts/start.sh   # first run creates .env and asks for a SiliconFlow API key; opens the browser once every service is healthy
./scripts/stop.sh    # stop (data is kept)
```

| URL | What |
|---|---|
| http://localhost:8080 | Web workspace |
| http://localhost:16686 | Jaeger tracing |
| http://localhost:19090 | Prometheus |

The script avoids ports that are already taken, so check its output for the actual addresses. Try it with `docs/samples/binary-tree-demo.mp4` (a 30-second sample); one analysis takes roughly 1 to 3 minutes depending on how fast the model responds.

### Local development

```bash
cp .env.example .env                   # fill in passwords and SILICONFLOW_API_KEY
./scripts/dev-up.sh                    # start middleware only (MySQL/Redis/Kafka/MinIO/Qdrant/Jaeger/Prometheus)
set -a; source .env; set +a
(cd agent-py && uv run python -m app.server) &
(cd server-go && go run ./cmd/server) &
(cd client && npm ci && npm run dev)   # http://localhost:5173
```

After changing `proto/`, run `./scripts/gen-proto.sh` to regenerate the Go and Python stubs (generated code is committed, and CI checks that it matches the proto).

## Repository layout

```text
Vparser
├── proto/               # gRPC contract (single source of truth)
├── server-go/           # Go gateway: HTTP API, Kafka, locks, rate limiting, VideoContext building
├── agent-py/            # Python agent: workflow, retrieval, evidence checking, checkpoints
├── client/              # Vue 3 workspace (served by nginx)
├── deploy/              # Prometheus configuration
├── docs/                # architecture & contract, sample video
├── scripts/             # one-click start/stop, middleware, stub generation
└── docker-compose.yml
```

## Tests

```bash
(cd server-go && go test -race ./...)
(cd agent-py && uv run pytest)
(cd client && npm test && npm run build)
```

## License

[MIT](LICENSE)
