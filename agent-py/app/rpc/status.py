"""Exception -> gRPC status code (contract §2).

The code tells the gateway what to do next, so the order of checks matters: budget exhaustion is final
even though BudgetExceededError is an AgentError; "not ready" and deadlines are checked before the generic
permanent / transient split.
"""
import grpc
import httpx
import openai
import redis.exceptions
import sqlalchemy.exc

from ..errors import (
    BudgetExceededError,
    DeadlineExceededError,
    RetriableModelError,
    VideoContextNotReadyError,
    find_cause,
    is_permanent_failure,
)
from ..llm.deepseek import is_retriable_model_failure

# Infrastructure failures that are expected to heal on their own.
TRANSIENT_TYPES = (
    RetriableModelError,
    ConnectionError,
    TimeoutError,
    redis.exceptions.ConnectionError,
    redis.exceptions.TimeoutError,
    redis.exceptions.BusyLoadingError,
    sqlalchemy.exc.OperationalError,
    sqlalchemy.exc.DisconnectionError,
    sqlalchemy.exc.TimeoutError,
    httpx.TransportError,
    openai.APIConnectionError,
)


def status_for(error: BaseException) -> grpc.StatusCode:
    if find_cause(error, BudgetExceededError) is not None:
        return grpc.StatusCode.RESOURCE_EXHAUSTED
    if find_cause(error, VideoContextNotReadyError) is not None:
        return grpc.StatusCode.FAILED_PRECONDITION
    if find_cause(error, DeadlineExceededError) is not None:
        return grpc.StatusCode.DEADLINE_EXCEEDED
    if is_permanent_failure(error):
        return grpc.StatusCode.INVALID_ARGUMENT
    if find_cause(error, TRANSIENT_TYPES) is not None or is_retriable_model_failure(error):
        return grpc.StatusCode.UNAVAILABLE
    return grpc.StatusCode.INTERNAL
