"""Domain error taxonomy.

The RPC layer maps these to gRPC status codes (see app/rpc/status.py). Classification walks the
``__cause__`` chain, so a low-level failure keeps its meaning when a higher layer wraps it with context.
"""


class InvalidArgumentError(Exception):
    """Bad input, or a failure that will never succeed on retry (permanent)."""


class AgentError(Exception):
    """A step of the agent failed; retrying the whole operation may succeed."""


class VideoContextNotReadyError(Exception):
    def __init__(self) -> None:
        super().__init__("视频内容尚未解析完成，请先完成一次 Video Agent 分析")


class DeadlineExceededError(AgentError):
    """The execution budget's time limit was reached."""


class BudgetExceededError(AgentError):
    """The run was terminated by its time / token / cost budget."""


class RetriableModelError(Exception):
    """A model call failed in a way worth retrying (timeout, 429, 5xx, pool busy, connection)."""


class NonRetriableModelError(Exception):
    """A model call failed in a way retrying cannot fix."""


PERMANENT_TYPES = (InvalidArgumentError,)


def cause_chain(error: BaseException | None, max_depth: int = 8):
    current = error
    for _ in range(max_depth):
        if current is None:
            return
        yield current
        nxt = current.__cause__
        if nxt is current:
            return
        current = nxt


def find_cause(error: BaseException, types, max_depth: int = 16):
    for current in cause_chain(error, max_depth):
        if isinstance(current, types):
            return current
    return None


def is_permanent_failure(error: BaseException) -> bool:
    return find_cause(error, PERMANENT_TYPES) is not None
