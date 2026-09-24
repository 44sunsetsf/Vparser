"""DeepSeekClient: chat completions on an OpenAI-compatible endpoint (SiliconFlow).

Every call goes through one path: a bounded worker pool, a timeout capped by the remaining execution
budget, up to 3 attempts with exponential backoff for transient failures, and one strict-JSON retry for
structured outputs."""
import concurrent.futures
import logging
import threading
import time
from collections.abc import Callable
from typing import TypeVar

import httpx
import openai
from openai import OpenAI
from pydantic import BaseModel
from pydantic_core import to_json

from ..agent.budget import remaining_millis
from ..errors import (
    DeadlineExceededError,
    InvalidArgumentError,
    AgentError,
    NonRetriableModelError,
    RetriableModelError,
    cause_chain,
    find_cause,
)
from ..textutil import is_blank, trim
from ..models.dto import (
    AgentPlan,
    AnalysisResult,
    ChunkSummary,
    CriticResult,
    ModeClassification,
    VideoContext,
    VideoRetrievalIntent,
    VideoSegment,
)
from ..observability import LLM_CALL_DURATION, LLM_TOKENS, span
from ..telemetry import AgentTelemetry, estimate_tokens
from . import prompts as P

log = logging.getLogger("agent.llm")

MAX_MODEL_ATTEMPTS = 3
T = TypeVar("T", bound=BaseModel)


def _json(value) -> str:
    """Compact camelCase JSON with explicit nulls, as embedded in prompts."""
    if value is None:
        return "null"
    return to_json(value, by_alias=True).decode("utf-8")


class ModelCallExecutor:
    """Bounded pool for model calls: 8 workers and at most 28 calls in flight. Beyond that a call is
    rejected at once (RetriableModelError) instead of queueing past its deadline."""

    def __init__(self, workers: int = 8, queue: int = 20) -> None:
        self._pool = concurrent.futures.ThreadPoolExecutor(max_workers=workers, thread_name_prefix="LLM-Thread-")
        self._slots = threading.BoundedSemaphore(workers + queue)

    def submit(self, fn: Callable[[], str]) -> concurrent.futures.Future:
        if not self._slots.acquire(blocking=False):
            raise RetriableModelError("模型调用线程池繁忙")
        try:
            future = self._pool.submit(fn)
        except BaseException:
            self._slots.release()
            raise
        future.add_done_callback(lambda _f: self._slots.release())
        return future


def is_retriable_model_failure(error: BaseException) -> bool:
    for current in cause_chain(error, 8):
        if isinstance(current, NonRetriableModelError):
            return False
        if isinstance(current, RetriableModelError):
            return True
        if isinstance(current, openai.APIStatusError):
            status = current.status_code
            return status == 408 or status == 429 or status >= 500
        if isinstance(current, (openai.APIConnectionError, httpx.TransportError, TimeoutError,
                                ConnectionError)):
            return True
    return False


class DeepSeekClient:
    def __init__(self, settings, telemetry: AgentTelemetry,
                 executor: ModelCallExecutor | None = None,
                 chat_fn: Callable[[str, str], str | None] | None = None) -> None:
        timeout_seconds = settings.llm_timeout_seconds
        in_price, out_price = settings.llm_input_price_per_million, settings.llm_output_price_per_million
        if timeout_seconds < 1:
            raise InvalidArgumentError("模型超时时间必须大于 0")
        if in_price < 0 or out_price < 0:
            raise InvalidArgumentError("模型 Token 单价不能为负数")
        if settings.agent_max_estimated_cost > 0 and (in_price == 0 or out_price == 0):
            raise InvalidArgumentError("启用 Agent 成本预算时必须配置输入和输出 Token 单价")
        self._telemetry = telemetry
        self._executor = executor or ModelCallExecutor()
        self._model_timeout_ms = timeout_seconds * 1000
        self._in_price = in_price
        self._out_price = out_price
        self._model = settings.llm_model
        if chat_fn is None:
            # max_retries=0:重试策略由 _chat 统一处理,避免嵌套重试
            client = OpenAI(base_url=settings.siliconflow_base_url,
                            api_key=settings.siliconflow_api_key or "missing-api-key",
                            timeout=float(timeout_seconds), max_retries=0)

            def chat_fn(system: str, user: str) -> str | None:  # noqa: F811
                resp = client.chat.completions.create(
                    model=self._model,
                    messages=[{"role": "system", "content": system}, {"role": "user", "content": user}])
                return resp.choices[0].message.content

        self._chat_fn = chat_fn

    # ---------- 各角色 ----------
    def plan(self, context: VideoContext, mode_instruction: str = "") -> AgentPlan:
        try:
            prompt = (P.PLAN_HEAD + _json(context)
                      + P.mode_suffix("本次分析模式的额外拆解要求：", mode_instruction))
            return self._structured_chat("PLANNER", prompt, AgentPlan)
        except Exception as e:
            raise AgentError("Agent 任务规划失败") from e

    def replan(self, context: VideoContext, current_plan: AgentPlan, critique: CriticResult,
               mode_instruction: str = "") -> AgentPlan:
        try:
            prompt = (P.REPLAN_HEAD + _json(current_plan) + P.REPLAN_CRITIC + _json(critique)
                      + P.REPLAN_CONTEXT + _json(context)
                      + P.mode_suffix("本次分析模式的额外拆解要求：", mode_instruction))
            return self._structured_chat("REPLANNER", prompt, AgentPlan)
        except Exception as e:
            raise AgentError("Agent 任务重规划失败") from e

    def repair_plan(self, context: VideoContext, invalid_plan: AgentPlan,
                    mode_instruction: str = "") -> AgentPlan:
        try:
            prompt = (P.REPAIR_HEAD + _json(invalid_plan) + P.REPAIR_CONTEXT + _json(context)
                      + P.mode_suffix("本次分析模式的额外拆解要求：", mode_instruction))
            return self._structured_chat("PLANNER_REPAIR", prompt, AgentPlan)
        except Exception as e:
            raise AgentError("Agent 任务计划修复失败") from e

    def plan_retrieval(self, goal: str) -> VideoRetrievalIntent:
        try:
            return self._structured_chat("RETRIEVAL_PLANNER", P.RETRIEVAL_HEAD + goal, VideoRetrievalIntent)
        except Exception as e:
            raise AgentError("视频检索目标拆解失败") from e

    def classify_mode(self, goal: str) -> ModeClassification:
        try:
            return self._structured_chat("MODE_ROUTER", P.CLASSIFY_HEAD + goal, ModeClassification)
        except Exception as e:
            raise AgentError("意图路由分类失败") from e

    def summarize_chunk(self, segments: list[VideoSegment]) -> ChunkSummary:
        try:
            prompt = P.SUMMARIZE_HEAD + _json(segments)
            return self._parse_json(self._chat("CHUNK_SUMMARY", prompt), ChunkSummary)
        except Exception as e:
            raise AgentError("视频片段摘要失败") from e

    def execute(self, context: VideoContext, plan: AgentPlan, previous_critique: CriticResult | None,
                mode_instruction: str = "") -> AnalysisResult:
        try:
            prompt = (P.EXECUTE_HEAD + _json(plan) + P.EXECUTE_PREVIOUS_CRITIQUE + _json(previous_critique)
                      + P.EXECUTE_CONTEXT + _json(context) + P.execute_suffix(mode_instruction))
            return self._structured_chat("EXECUTOR", prompt, AnalysisResult)
        except Exception as e:
            raise AgentError("Agent 执行失败") from e

    def critique(self, context: VideoContext, plan: AgentPlan, result: AnalysisResult,
                 mode_instruction: str = "") -> CriticResult:
        try:
            prompt = (P.CRITIQUE_HEAD + _json(plan) + P.CRITIQUE_DRAFT + _json(result)
                      + P.CRITIQUE_CONTEXT + _json(context)
                      + P.mode_suffix("本次审查模式的额外校验要求：", mode_instruction))
            return self._structured_chat("CRITIC", prompt, CriticResult)
        except Exception as e:
            raise AgentError("Critic 校验失败") from e

    def answer_follow_up(self, prompt: str) -> str:
        """Free-form Markdown answer (no JSON contract); retries and budget apply as for every call."""
        return self._chat("FOLLOW_UP", prompt)

    # ---------- 解析 / 对话 ----------
    @staticmethod
    def _parse_json(response: str | None, type_: type[T]) -> T:
        if response is None or is_blank(response):
            raise AgentError("模型返回空响应")
        text = trim(response.replace("```json", "").replace("```", ""))
        start = text.find("{")
        end = text.rfind("}")
        if start < 0 or end <= start:
            raise AgentError("模型未返回 JSON 对象")
        return type_.model_validate_json(text[start:end + 1])

    def _structured_chat(self, stage: str, prompt: str, type_: type[T]) -> T:
        response = self._chat(stage, prompt)
        try:
            return self._parse_json(response, type_)
        except Exception:
            self._telemetry.increment_current("structuredOutputRetries", 1)
            return self._parse_json(self._chat(stage, prompt + P.STRICT_JSON_RETRY_SUFFIX), type_)

    def _chat(self, stage: str, prompt: str) -> str:
        last_error: Exception | None = None
        input_tokens = estimate_tokens(P.SYSTEM_POLICY + "\n" + prompt)
        for attempt in range(MAX_MODEL_ATTEMPTS):
            started = time.monotonic_ns()
            outcome = "error"
            with span("llm.call", **{"agent.stage": stage, "llm.attempt": attempt + 1,
                                     "llm.model": self._model,
                                     "llm.estimated_input_tokens": input_tokens}) as s:
                try:
                    response = self._invoke_model(prompt)
                    if response is None or is_blank(response):
                        raise RetriableModelError("模型返回空响应")
                    outcome = "ok"
                    output_tokens = estimate_tokens(response)
                    s.set_attribute("llm.estimated_output_tokens", output_tokens)
                    LLM_TOKENS.labels(stage, "input").inc(input_tokens)
                    LLM_TOKENS.labels(stage, "output").inc(output_tokens)
                    self._telemetry.model_call(stage, P.SYSTEM_POLICY + "\n" + prompt, response,
                                               self._in_price, self._out_price, started)
                    return response
                except DeadlineExceededError:
                    # Out of budget: no retry, and not a model fault — let the caller classify it.
                    outcome = "deadline"
                    self._telemetry.increment_current("modelCallFailures", 1)
                    self._telemetry.fail_current_stage(stage, started)
                    raise
                except Exception as e:
                    last_error = e
                    outcome = "timeout" if find_cause(e, TimeoutError) is not None else "error"
                    s.set_attribute("agent.outcome", outcome)
                    self._telemetry.increment_current("modelCallFailures", 1)
                    retriable = is_retriable_model_failure(e)
                    if not retriable or attempt == MAX_MODEL_ATTEMPTS - 1:
                        self._telemetry.fail_current_stage(stage, started)
                        if not retriable:
                            raise InvalidArgumentError("模型请求不可重试") from e
                        break
                finally:
                    LLM_CALL_DURATION.labels(stage, outcome).observe(
                        (time.monotonic_ns() - started) / 1_000_000_000)
            self._wait_before_retry(attempt)
        raise AgentError("模型调用达到最大重试次数") from last_error

    def _invoke_model(self, prompt: str) -> str | None:
        remaining_budget_ms = remaining_millis()
        timeout_ms = min(self._model_timeout_ms, remaining_budget_ms)
        future = self._executor.submit(lambda: self._chat_fn(P.SYSTEM_POLICY, prompt))
        try:
            return future.result(timeout=timeout_ms / 1000)
        except concurrent.futures.TimeoutError as e:
            future.cancel()
            if remaining_budget_ms <= self._model_timeout_ms:
                raise DeadlineExceededError("模型调用超过 Agent 剩余时间预算") from e
            raise RetriableModelError("模型调用超时") from e

    @staticmethod
    def _wait_before_retry(attempt: int) -> None:
        time.sleep((1_000 << attempt) / 1000)
