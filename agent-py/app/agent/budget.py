"""Execution time budget, scoped with contextvars. Nested scopes can only tighten the deadline, so the
RPC layer (caller deadline) and the agent loop (AGENT_MAX_DURATION_MS) compose by taking the minimum."""
import time
from contextlib import contextmanager
from contextvars import ContextVar

from ..errors import DeadlineExceededError, InvalidArgumentError

_DEADLINE_NANOS: ContextVar[int | None] = ContextVar("agent_deadline_nanos", default=None)
LONG_MAX = 2**63 - 1


@contextmanager
def open_budget(max_duration_ms: int):
    if max_duration_ms < 1:
        raise InvalidArgumentError("Agent 执行时长预算必须大于 0")
    previous = _DEADLINE_NANOS.get()
    requested = time.monotonic_ns() + max_duration_ms * 1_000_000
    token = _DEADLINE_NANOS.set(requested if previous is None else min(previous, requested))
    try:
        yield
    finally:
        _DEADLINE_NANOS.reset(token)


def remaining_millis() -> int:
    deadline = _DEADLINE_NANOS.get()
    if deadline is None:
        return LONG_MAX
    remaining = deadline - time.monotonic_ns()
    if remaining <= 0:
        raise DeadlineExceededError("Agent 已耗尽执行时长预算")
    return max(1, remaining // 1_000_000)


def check(stage: str) -> None:
    try:
        remaining_millis()
    except DeadlineExceededError as e:
        raise DeadlineExceededError(f"{stage} 后终止：{e}") from None
