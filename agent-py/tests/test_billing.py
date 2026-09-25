from datetime import datetime, timezone
from types import SimpleNamespace

import fakeredis
import grpc

from app.billing import CURRENT_USER, HEADER, BillingInterceptor, SpendLedger, key


def test_key_follows_beijing_day():
    # 17:00 UTC is already the next day in Beijing; server-go uses the same rule
    assert key(7, datetime(2026, 9, 25, 17, 0, tzinfo=timezone.utc)) == "billing:spend:7:2026-09-26"


def test_ledger_adds_only_for_a_known_user():
    r = fakeredis.FakeRedis(decode_responses=True)
    ledger = SpendLedger(r)
    ledger.add(0.5, 100, 10)                     # no user: no spend counter, but usage is still recorded as "-"
    assert r.keys("billing:spend:*") == []
    [u] = r.keys("billing:usage:*")
    assert r.hget(u, "calls:-") == "1" and r.hget(u, "in:-") == "100"
    token = CURRENT_USER.set(2)
    try:
        ledger.add(0.25)
        ledger.add(0.5)
        ledger.add(0)
    finally:
        CURRENT_USER.reset(token)
    [k] = r.keys("billing:spend:2:*")
    assert float(r.get(k)) == 0.75
    [u] = r.keys("billing:usage:*")
    assert r.hget(u, "calls") == "4" and r.hget(u, "calls:2") == "3"
    assert abs(float(r.hget(u, "cost:2")) - 0.75) < 1e-9
    assert 0 < r.ttl(k) <= 3 * 86400


def test_ledger_never_raises():
    class Broken:
        def pipeline(self):
            raise ConnectionError("redis down")
    token = CURRENT_USER.set(2)
    try:
        SpendLedger(Broken()).add(1.0)
    finally:
        CURRENT_USER.reset(token)


def test_interceptor_sets_user_for_the_call():
    seen = []
    handler = grpc.unary_unary_rpc_method_handler(lambda req, ctx: seen.append(CURRENT_USER.get()) or "ok")
    details = SimpleNamespace(method="/agent.v1.AgentService/Run", invocation_metadata=((HEADER, "42"),))
    wrapped = BillingInterceptor().intercept_service(lambda d: handler, details)
    assert wrapped.unary_unary(None, None) == "ok"
    assert seen == [42] and CURRENT_USER.get() is None
    plain = SimpleNamespace(method="/agent.v1.AgentService/Run", invocation_metadata=())
    assert BillingInterceptor().intercept_service(lambda d: handler, plain) is handler
