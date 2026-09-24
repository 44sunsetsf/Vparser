"""Tracing and metrics (contract §8).

Tracing: OpenTelemetry with the W3C trace-context propagator. The gRPC server interceptor continues the
gateway's trace; Redis / PyMySQL / httpx (which also carries model calls) are auto-instrumented. With
OTEL_EXPORTER_OTLP_ENDPOINT unset no provider is installed and every span is a no-op.

Metrics: Prometheus, served by prometheus_client on AGENT_METRICS_PORT at /metrics.
"""
import logging
import time
from collections.abc import Iterator
from contextlib import contextmanager

from opentelemetry import propagate, trace
from opentelemetry.baggage.propagation import W3CBaggagePropagator
from opentelemetry.propagators.composite import CompositePropagator
from opentelemetry.trace import Status, StatusCode
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator
from prometheus_client import Counter, Histogram, start_http_server

log = logging.getLogger("agent.observability")

_tracer = trace.get_tracer("agent-py")

# ---------- metrics (names and labels are part of the contract) ----------
AGENT_RUN_DURATION = Histogram(
    "agent_run_duration_seconds", "Run RPC latency.", ["mode", "outcome"],
    buckets=(1, 5, 10, 20, 30, 60, 90, 120, 180, 300, 600))
LLM_CALL_DURATION = Histogram(
    "llm_call_duration_seconds", "Latency of a single model call attempt.", ["stage", "outcome"],
    buckets=(0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300))
LLM_TOKENS = Counter("llm_tokens_total", "Estimated model tokens.", ["stage", "direction"])
CRITIC_ROUNDS = Counter("agent_critic_rounds_total", "Critic rounds by verdict.", ["passed"])
BUDGET_TERMINATIONS = Counter("agent_budget_terminations_total", "Runs stopped by a budget.", ["reason"])
VECTOR_FALLBACKS = Counter("retrieval_vector_fallback_total",
                           "Vector store failures served from in-memory vectors / keywords instead.")


# ---------- tracing ----------
@contextmanager
def span(name: str, **attributes) -> Iterator[trace.Span]:
    """A manual span. Sets agent.outcome=ok unless the body set its own outcome; on an exception
    records it and marks the span as an error."""
    with _tracer.start_as_current_span(name, record_exception=False, set_status_on_exception=False) as s:
        for key, value in attributes.items():
            if value is not None:
                s.set_attribute(key, value)
        try:
            yield s
        except BaseException as e:
            s.set_attribute("agent.outcome", "error")
            s.record_exception(e)
            s.set_status(Status(StatusCode.ERROR, type(e).__name__))
            raise
        if "agent.outcome" not in (getattr(s, "attributes", None) or {}):
            s.set_attribute("agent.outcome", "ok")


def setup_tracing(endpoint: str, service_name: str) -> bool:
    """Installs the tracer provider and auto-instrumentation. Returns False when tracing is disabled."""
    propagate.set_global_textmap(CompositePropagator(
        [TraceContextTextMapPropagator(), W3CBaggagePropagator()]))
    if not endpoint:
        return False
    from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
    from opentelemetry.sdk.resources import SERVICE_NAME, Resource
    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import BatchSpanProcessor

    provider = TracerProvider(resource=Resource.create({SERVICE_NAME: service_name}))
    # Collectors inside the cluster (e.g. jaeger:4317) speak plaintext unless an https:// URL is given.
    provider.add_span_processor(BatchSpanProcessor(
        OTLPSpanExporter(endpoint=endpoint, insecure=not endpoint.startswith("https://"))))
    trace.set_tracer_provider(provider)
    _instrument_clients()
    log.info("tracing enabled endpoint=%s service=%s", endpoint, service_name)
    return True


def _instrument_clients() -> None:
    instrumentors = (
        ("opentelemetry.instrumentation.redis", "RedisInstrumentor"),
        ("opentelemetry.instrumentation.pymysql", "PyMySQLInstrumentor"),
        ("opentelemetry.instrumentation.httpx", "HTTPXClientInstrumentor"),
    )
    for module_name, class_name in instrumentors:
        try:
            module = __import__(module_name, fromlist=[class_name])
            getattr(module, class_name)().instrument()
        except Exception:
            log.warning("instrumentation unavailable: %s", module_name, exc_info=True)


def grpc_server_interceptor():
    """OpenTelemetry server interceptor: extracts traceparent from metadata, one span per RPC."""
    from opentelemetry.instrumentation.grpc import server_interceptor
    return server_interceptor()


def shutdown_tracing() -> None:
    provider = trace.get_tracer_provider()
    shutdown = getattr(provider, "shutdown", None)
    if shutdown is not None:
        shutdown()


def start_metrics_server(port: int) -> None:
    start_http_server(port)
    log.info("metrics listening on :%s/metrics", port)


class Stopwatch:
    def __init__(self) -> None:
        self._started = time.perf_counter()

    def seconds(self) -> float:
        return time.perf_counter() - self._started
