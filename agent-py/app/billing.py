"""Per-user daily model spend, the counterpart of server-go/internal/billing.

server-go tags each call with the user it is working for (gRPC metadata ``x-billing-user``);
``BillingInterceptor`` puts that id in a context variable for the RPC, and ``SpendLedger`` adds the
estimated cost (CNY) of every model call to ``billing:spend:<user>:<Beijing date>`` in Redis.
server-go reads the same key to refuse new work once a user's daily allowance is used up.
Every call is also added to ``billing:usage:<Beijing date>`` (calls / in / out tokens / cost, in total and per user,
user "-" when unknown) for the owner's analytics page.
Calls without a user (offline evaluation, for instance) count towards usage but never towards anyone's allowance.
"""
import logging
from contextvars import ContextVar
from datetime import datetime, timedelta, timezone

import grpc

HEADER = "x-billing-user"
ZONE = timezone(timedelta(hours=8))          # keep in step with server-go: the day follows Beijing time
KEEP = timedelta(days=3)                    # counters only matter for today; expire them soon after
USAGE_KEEP = timedelta(days=120)            # daily usage totals for the analytics page

CURRENT_USER: ContextVar[int | None] = ContextVar("billing_user", default=None)
log = logging.getLogger("agent.billing")


def key(user_id: int, now: datetime | None = None) -> str:
    day = (now or datetime.now(timezone.utc)).astimezone(ZONE).strftime("%Y-%m-%d")
    return f"billing:spend:{user_id}:{day}"


def usage_key(now: datetime | None = None) -> str:
    day = (now or datetime.now(timezone.utc)).astimezone(ZONE).strftime("%Y-%m-%d")
    return f"billing:usage:{day}"


class SpendLedger:
    def __init__(self, redis_client) -> None:
        self._redis = redis_client

    def add(self, cost: float, tokens_in: int = 0, tokens_out: int = 0) -> None:
        user = CURRENT_USER.get()
        who = "-" if user is None else str(user)
        try:
            pipe = self._redis.pipeline()
            if user is not None and cost > 0:
                k = key(user)
                pipe.incrbyfloat(k, cost)
                pipe.expire(k, KEEP)
            u = usage_key()
            for field, n in (("calls", 1), ("in", tokens_in), ("out", tokens_out),
                             (f"calls:{who}", 1), (f"in:{who}", tokens_in), (f"out:{who}", tokens_out)):
                pipe.hincrby(u, field, int(n))
            pipe.hincrbyfloat(u, "cost", cost)
            pipe.hincrbyfloat(u, f"cost:{who}", cost)
            pipe.expire(u, USAGE_KEEP)
            pipe.execute()
        except Exception:   # never fail an analysis because the ledger is down
            log.warning("billing_ledger_write_failed user=%s cost=%.6f", user, cost, exc_info=True)


class BillingInterceptor(grpc.ServerInterceptor):
    """Runs each unary handler with CURRENT_USER set from the call's metadata."""

    def intercept_service(self, continuation, handler_call_details):
        handler = continuation(handler_call_details)
        raw = next((value for k, value in handler_call_details.invocation_metadata or () if k == HEADER), None)
        if handler is None or handler.unary_unary is None or not isinstance(raw, str) or not raw.isdigit():
            return handler
        user, inner = int(raw), handler.unary_unary

        def behavior(request, context):
            token = CURRENT_USER.set(user)
            try:
                return inner(request, context)
            finally:
                CURRENT_USER.reset(token)

        return grpc.unary_unary_rpc_method_handler(behavior, request_deserializer=handler.request_deserializer,
                                                   response_serializer=handler.response_serializer)
