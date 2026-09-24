"""AgentTelemetry: per-analysis stage timings and counters, accumulated in process and persisted to
Redis as a whole snapshot (agent-py is the only writer of trace snapshots)."""
import json
import logging
import threading
import time
import uuid
from contextvars import ContextVar
from dataclasses import dataclass, field
from datetime import UTC, datetime

from .keys import goal_digest
from .models.dto import AnalysisMode, format_instant

log = logging.getLogger("agent.telemetry")

MAX_TRACES = 500
TRACE_TTL_SECONDS = 7 * 24 * 3600


@dataclass
class BudgetUsage:
    estimated_tokens: int
    estimated_cost: float


@dataclass
class _TraceData:
    trace_id: str
    task_id: int
    goal_digest: str
    task_key: str
    started_at: datetime = field(default_factory=lambda: datetime.now(UTC))
    stage_durations: dict[str, int] = field(default_factory=dict)
    counters: dict[str, int] = field(default_factory=dict)
    values: dict[str, float] = field(default_factory=dict)
    estimated_cost: float = 0.0

    def increment(self, metric: str, amount: int) -> None:
        self.counters[metric] = self.counters.get(metric, 0) + amount

    def counter_value(self, metric: str) -> int:
        return self.counters.get(metric, 0)

    def snapshot(self) -> dict:
        return {
            "traceId": self.trace_id,
            "taskId": self.task_id,
            "goalDigest": self.goal_digest,
            "startedAt": format_instant(self.started_at),
            "stageDurationMs": dict(self.stage_durations),
            "counters": dict(self.counters),
            "values": dict(self.values),
            "estimatedCost": self.estimated_cost,
        }


def estimate_tokens(text: str | None) -> int:
    if not text:
        return 0
    non_ascii = sum(1 for c in text if ord(c) > 127)
    ascii_count = len(text) - non_ascii
    return max(1, non_ascii + (ascii_count + 3) // 4)


class AgentTelemetry:
    def __init__(self, redis_client) -> None:
        self._redis = redis_client
        self._lock = threading.RLock()
        self._traces: dict[str, _TraceData] = {}
        self._latest_by_task: dict[str, str] = {}
        self._current: ContextVar[str | None] = ContextVar("agent_current_trace", default=None)

    # ---- 生命周期 ----
    def start(self, task_id: int, goal: str, mode: AnalysisMode = AnalysisMode.GENERAL,
              trace_id: str | None = None) -> str:
        digest = goal_digest(goal, mode)
        task_key = f"{task_id}:{digest}"
        with self._lock:
            if len(self._traces) >= MAX_TRACES:
                oldest = min(self._traces.values(), key=lambda t: t.started_at)
                self._traces.pop(oldest.trace_id, None)
                if self._latest_by_task.get(oldest.task_key) == oldest.trace_id:
                    del self._latest_by_task[oldest.task_key]
            trace_id = trace_id or str(uuid.uuid4())
            trace = _TraceData(trace_id, task_id, digest, task_key)
            self._traces[trace_id] = trace
            self._latest_by_task[task_key] = trace_id
            self._current.set(trace_id)
        self._persist(trace)
        log.info("agent_trace traceId=%s taskId=%s stage=START status=SUCCESS", trace_id, task_id)
        return trace_id

    def bind(self, trace_id: str) -> None:
        self._current.set(trace_id)

    def clear(self) -> None:
        self._current.set(None)

    def flush(self, trace_id: str) -> None:
        trace = self._traces.get(trace_id)
        if trace is not None:
            self._persist(trace)

    def apply_seed(self, trace_id: str, seed: dict | None) -> None:
        """合并网关传来的 telemetrySeed:stages {name:{durationMs,success}} 与 counters。"""
        trace = self._traces.get(trace_id)
        if trace is None or not seed:
            return
        with self._lock:
            for stage, info in (seed.get("stages") or {}).items():
                info = info or {}
                trace.stage_durations[stage] = trace.stage_durations.get(stage, 0) + int(info.get("durationMs", 0))
                trace.increment(stage + "Calls", 1)
                if not info.get("success", True):
                    trace.increment("failedStages", 1)
            for metric, amount in (seed.get("counters") or {}).items():
                trace.increment(metric, int(amount))

    # ---- 记录 ----
    def stage(self, trace_id: str, stage: str, started_nanos: int, success: bool) -> None:
        trace = self._traces.get(trace_id)
        if trace is None:
            return
        duration_ms = (time.monotonic_ns() - started_nanos) // 1_000_000
        with self._lock:
            trace.stage_durations[stage] = trace.stage_durations.get(stage, 0) + duration_ms
            trace.increment(stage + "Calls", 1)
            if not success:
                trace.increment("failedStages", 1)
        log.info("agent_trace traceId=%s taskId=%s stage=%s durationMs=%s status=%s",
                 trace_id, trace.task_id, stage, duration_ms, "SUCCESS" if success else "FAILED")
        self._persist(trace)

    def increment(self, trace_id: str | None, metric: str, amount: int) -> None:
        if trace_id is None:
            return
        trace = self._traces.get(trace_id)
        if trace is not None:
            with self._lock:
                trace.increment(metric, amount)

    def increment_current(self, metric: str, amount: int) -> None:
        trace_id = self._current.get()
        if trace_id is not None:
            self.increment(trace_id, metric, amount)

    def value_current(self, metric: str, value: float) -> None:
        trace_id = self._current.get()
        trace = self._traces.get(trace_id) if trace_id else None
        if trace is not None:
            trace.values[metric] = float(value)

    def fail_current_stage(self, stage: str, started_nanos: int) -> None:
        trace_id = self._current.get()
        if trace_id is not None:
            self.stage(trace_id, stage, started_nanos, False)

    def model_call(self, stage: str, prompt: str, response: str,
                   input_price_per_million: float, output_price_per_million: float,
                   started_nanos: int) -> None:
        trace_id = self._current.get()
        trace = self._traces.get(trace_id) if trace_id else None
        if trace is None:
            return
        input_tokens = estimate_tokens(prompt)
        output_tokens = estimate_tokens(response)
        with self._lock:
            trace.increment("modelCalls", 1)
            trace.increment("inputTokensEstimated", input_tokens)
            trace.increment("outputTokensEstimated", output_tokens)
            trace.estimated_cost += (input_tokens * input_price_per_million / 1_000_000
                                     + output_tokens * output_price_per_million / 1_000_000)
        self.stage(trace_id, stage, started_nanos, True)

    def current_usage(self) -> BudgetUsage:
        trace_id = self._current.get()
        trace = self._traces.get(trace_id) if trace_id else None
        if trace is None:
            return BudgetUsage(0, 0.0)
        return BudgetUsage(
            trace.counter_value("inputTokensEstimated") + trace.counter_value("outputTokensEstimated"),
            trace.estimated_cost)

    # ---- 查询 / 清理 ----
    def latest(self, task_id: int, goal: str, mode: AnalysisMode = AnalysisMode.GENERAL) -> dict:
        digest = goal_digest(goal, mode)
        task_key = f"{task_id}:{digest}"
        trace_id = self._latest_by_task.get(task_key)
        trace = self._traces.get(trace_id) if trace_id else None
        if trace is not None:
            return trace.snapshot()
        try:
            if trace_id is None:
                trace_id = self._redis.get(self._latest_trace_key(task_id, digest))
            snapshot = None if trace_id is None else self._redis.get(self._trace_key(trace_id))
            return {} if snapshot is None else json.loads(snapshot)
        except Exception:
            log.warning("agent_trace_read_failed taskId=%s", task_id, exc_info=True)
            return {}

    def delete_task(self, task_id: int) -> None:
        prefix = f"{task_id}:"
        trace_ids: set[str] = set()
        with self._lock:
            for key in [k for k in self._latest_by_task if k.startswith(prefix)]:
                trace_ids.add(self._latest_by_task.pop(key))
        try:
            latest_keys = self._redis.smembers(self._index_key(task_id))
            if latest_keys:
                for latest_key in latest_keys:
                    trace_id = self._redis.get(latest_key)
                    if trace_id is not None:
                        trace_ids.add(trace_id)
                self._redis.delete(*latest_keys)
            for trace_id in trace_ids:
                self._redis.delete(self._trace_key(trace_id))
            self._redis.delete(self._index_key(task_id))
        except Exception:
            log.warning("agent_trace_cleanup_failed taskId=%s", task_id, exc_info=True)
        with self._lock:
            for trace_id in trace_ids:
                self._traces.pop(trace_id, None)

    # ---- 内部 ----
    def _persist(self, trace: _TraceData) -> None:
        try:
            self._redis.set(self._trace_key(trace.trace_id),
                            json.dumps(trace.snapshot(), ensure_ascii=False, separators=(",", ":")),
                            ex=TRACE_TTL_SECONDS)
            latest_key = self._latest_trace_key(trace.task_id, trace.goal_digest)
            self._redis.set(latest_key, trace.trace_id, ex=TRACE_TTL_SECONDS)
            self._redis.sadd(self._index_key(trace.task_id), latest_key)
            self._redis.expire(self._index_key(trace.task_id), TRACE_TTL_SECONDS)
        except Exception:
            log.warning("agent_trace_persist_failed traceId=%s taskId=%s",
                        trace.trace_id, trace.task_id, exc_info=True)

    @staticmethod
    def _trace_key(trace_id: str) -> str:
        return f"agent:trace:{trace_id}"

    @staticmethod
    def _latest_trace_key(task_id: int, digest: str) -> str:
        return f"agent:trace:task:{task_id}:{digest}"

    @staticmethod
    def _index_key(task_id: int) -> str:
        return f"agent:trace:task:{task_id}:goals"
