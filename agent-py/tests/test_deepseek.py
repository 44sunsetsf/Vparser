from types import SimpleNamespace

import httpx
import openai
import pytest

from app.agent import budget
from app.errors import BudgetExceededError, InvalidArgumentError, AgentError, is_permanent_failure
from app.llm import deepseek as ds
from app.llm import prompts as P
from app.models.dto import AgentPlan, AnalysisMode, VideoContext, VideoSegment
from app.telemetry import AgentTelemetry, estimate_tokens
import fakeredis

SETTINGS = SimpleNamespace(llm_timeout_seconds=300, llm_input_price_per_million=0, llm_output_price_per_million=0,
                           agent_max_estimated_cost=0, llm_model="m", siliconflow_base_url="http://x",
                           siliconflow_api_key="k")
CTX = VideoContext(source="s", user_goal="g", segments=[VideoSegment(start_ms=0, end_ms=10, transcript="t")])
PLAN_JSON = '{"understoodGoal":"u","tasks":["a"]}'


def status_error(code: int) -> openai.APIStatusError:
    resp = httpx.Response(code, request=httpx.Request("POST", "http://x"))
    return openai.APIStatusError("boom", response=resp, body=None)


def make(chat_fn):
    redis = fakeredis.FakeRedis(decode_responses=True)
    tel = AgentTelemetry(redis)
    return ds.DeepSeekClient(SETTINGS, tel, chat_fn=chat_fn), tel


@pytest.fixture(autouse=True)
def no_sleep(monkeypatch):
    sleeps = []
    monkeypatch.setattr(ds.time, "sleep", lambda s: sleeps.append(s))
    return sleeps


def test_prompt_is_head_plus_json_and_system_policy_passed():
    seen = []

    def chat(system, user):
        seen.append((system, user))
        return PLAN_JSON

    client, _ = make(chat)
    assert client.plan(CTX) == AgentPlan(understood_goal="u", tasks=["a"])
    assert seen[0][0] == P.SYSTEM_POLICY
    assert seen[0][1] == P.PLAN_HEAD + CTX.to_json()


def test_retries_on_5xx_with_backoff_then_succeeds(no_sleep):
    calls = []

    def chat(system, user):
        calls.append(1)
        if len(calls) < 3:
            raise status_error(503)
        return PLAN_JSON

    client, tel = make(chat)
    trace = tel.start(1, "g")
    assert client.plan(CTX).tasks == ["a"]
    assert len(calls) == 3 and no_sleep == [1.0, 2.0]
    assert tel.latest(1, "g")["counters"]["modelCallFailures"] == 2


def test_gives_up_after_three_attempts_as_retryable_state_error():
    client, _ = make(lambda s, u: (_ for _ in ()).throw(status_error(429)))
    with pytest.raises(AgentError) as e:
        client.plan(CTX)
    assert not is_permanent_failure(e.value)


def test_non_retriable_4xx_is_permanent_and_not_retried():
    calls = []

    def chat(s, u):
        calls.append(1)
        raise status_error(401)

    client, _ = make(chat)
    with pytest.raises(AgentError) as e:
        client.plan(CTX)
    assert len(calls) == 1 and is_permanent_failure(e.value)


def test_empty_response_is_retried():
    answers = iter(["", "  ", PLAN_JSON])
    client, _ = make(lambda s, u: next(answers))
    assert client.plan(CTX).tasks == ["a"]


def test_structured_chat_retries_once_with_strict_json_suffix():
    prompts = []
    answers = iter(["not json", "```json\n" + PLAN_JSON + "\n```"])

    def chat(s, u):
        prompts.append(u)
        return next(answers)

    client, tel = make(chat)
    tel.start(1, "g")
    assert client.plan(CTX).understood_goal == "u"
    assert prompts[1] == prompts[0] + "\n请严格返回合法 JSON，不要添加解释或代码块。"
    assert tel.latest(1, "g")["counters"]["structuredOutputRetries"] == 1


def test_timeout_within_budget_becomes_deadline_then_budget_exceeded_via_chain():
    import threading

    def chat(s, u):
        threading.Event().wait(0.5)
        return PLAN_JSON

    client, _ = make(chat)
    with budget.open_budget(50):
        with pytest.raises(AgentError) as e:
            client.plan(CTX)
    from app.agent.loop import _find_deadline
    assert _find_deadline(e.value) is not None


def test_executor_rejects_when_saturated():
    ex = ds.ModelCallExecutor(workers=1, queue=0)
    import threading
    gate = threading.Event()
    f = ex.submit(lambda: gate.wait(2) and "x")
    with pytest.raises(ds.RetriableModelError):
        ex.submit(lambda: "y")
    gate.set()
    f.result()


def test_estimate_tokens():
    assert estimate_tokens("") == 0
    assert estimate_tokens("abcd") == 1
    assert estimate_tokens("abcde") == 2
    assert estimate_tokens("中文ab") == 2 + 1


def test_connection_errors_are_retried(no_sleep):
    calls = []

    def chat(s, u):
        calls.append(1)
        if len(calls) < 2:
            raise openai.APIConnectionError(request=httpx.Request("POST", "http://x"))
        return PLAN_JSON

    client, _ = make(chat)
    assert client.plan(CTX).tasks == ["a"] and len(calls) == 2


def test_exhausted_budget_is_not_reported_as_permanent_model_failure():
    client, _ = make(lambda s, u: PLAN_JSON)
    import threading
    with budget.open_budget(1):
        threading.Event().wait(0.005)  # time.sleep is stubbed out by the no_sleep fixture
        with pytest.raises(AgentError) as e:
            client.plan(CTX)
    assert not is_permanent_failure(e.value)


class _RecordingOpenAI:
    """Stands in for openai.OpenAI and records the kwargs of each chat completion call."""
    calls: list = []

    def __init__(self, **_):
        self.chat = SimpleNamespace(completions=SimpleNamespace(create=self._create))

    def _create(self, **kwargs):
        _RecordingOpenAI.calls.append(kwargs)
        return SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content=PLAN_JSON))])


@pytest.mark.parametrize("thinking, expected", [(None, None), (False, {"enable_thinking": False})])
def test_enable_thinking_is_only_sent_when_configured(monkeypatch, thinking, expected):
    _RecordingOpenAI.calls = []
    monkeypatch.setattr(ds, "OpenAI", _RecordingOpenAI)
    settings = SimpleNamespace(**vars(SETTINGS), llm_enable_thinking=thinking)
    client = ds.DeepSeekClient(settings, AgentTelemetry(fakeredis.FakeRedis(decode_responses=True)))
    client.plan(CTX)
    assert _RecordingOpenAI.calls and _RecordingOpenAI.calls[0]["extra_body"] == expected


def test_model_calls_are_charged_to_the_billing_user():
    from app.billing import CURRENT_USER, SpendLedger
    redis = fakeredis.FakeRedis(decode_responses=True)
    settings = SimpleNamespace(**{**vars(SETTINGS), "llm_input_price_per_million": 1.8,
                                  "llm_output_price_per_million": 10.8})
    client = ds.DeepSeekClient(settings, AgentTelemetry(redis), chat_fn=lambda s, u: PLAN_JSON,
                               ledger=SpendLedger(redis))
    token = CURRENT_USER.set(2)
    try:
        client.plan(CTX)
    finally:
        CURRENT_USER.reset(token)
    [key] = redis.keys("billing:spend:2:*")
    assert 0 < float(redis.get(key)) < 0.01      # one short call costs a fraction of a fen
