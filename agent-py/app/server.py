"""gRPC entry point: ``python -m app.server``.

Startup: tracing and metrics first, then the composition root, then the server. The health status stays
NOT_SERVING until Redis and MySQL answer, so orchestrators route traffic only to a ready instance.
SIGTERM flips health to NOT_SERVING and drains in-flight RPCs for AGENT_GRPC_SHUTDOWN_GRACE_SECONDS.
"""
import logging
import signal
import sys
import threading
from concurrent import futures

import grpc
from grpc_health.v1 import health, health_pb2, health_pb2_grpc
from sqlalchemy import text

from .agent.evaluation import run_offline_evaluation
from .config import Settings, get_settings
from .container import Container, build_container
from .gen.agent.v1 import agent_pb2, agent_pb2_grpc
from .observability import grpc_server_interceptor, setup_tracing, shutdown_tracing, start_metrics_server
from .billing import BillingInterceptor
from .rpc.auth import TokenAuthInterceptor
from .rpc.servicer import AgentServicer

log = logging.getLogger("agent.server")

SERVICE_NAME = agent_pb2.DESCRIPTOR.services_by_name["AgentService"].full_name
READINESS_RETRY_SECONDS = 2.0


def build_server(container: Container, settings: Settings, *, port: int | None = None,
                 tracing: bool = False) -> tuple[grpc.Server, health.HealthServicer, int]:
    interceptors = (([grpc_server_interceptor()] if tracing else [])
                    + [TokenAuthInterceptor(settings.internal_token), BillingInterceptor()])
    server = grpc.server(
        futures.ThreadPoolExecutor(max_workers=settings.agent_grpc_max_workers, thread_name_prefix="rpc"),
        interceptors=interceptors,
        # Beyond this many in-flight RPCs grpc answers RESOURCE_EXHAUSTED at once instead of queueing.
        maximum_concurrent_rpcs=settings.grpc_max_concurrent_rpcs,
        options=[("grpc.so_reuseport", 0),
                 # The gateway pings idle connections every 60s; accept that instead of GOAWAY "too many pings".
                 ("grpc.keepalive_permit_without_calls", 1),
                 ("grpc.http2.min_ping_interval_without_data_ms", 30000)])
    agent_pb2_grpc.add_AgentServiceServicer_to_server(AgentServicer(container, settings), server)
    health_servicer = health.HealthServicer()
    for name in ("", SERVICE_NAME):
        health_servicer.set(name, health_pb2.HealthCheckResponse.NOT_SERVING)
    health_pb2_grpc.add_HealthServicer_to_server(health_servicer, server)
    bound = server.add_insecure_port(f"[::]:{settings.agent_grpc_port if port is None else port}")
    if bound == 0:
        raise RuntimeError("failed to bind gRPC port")
    return server, health_servicer, bound


def check_dependencies(container: Container) -> None:
    container.redis.ping()
    with container.engine.connect() as conn:
        conn.execute(text("SELECT 1"))


def mark_serving_when_ready(container: Container, health_servicer: health.HealthServicer,
                            stopping: threading.Event) -> None:
    attempts = 0
    while not stopping.is_set():
        try:
            check_dependencies(container)
        except Exception as e:
            attempts += 1
            if attempts == 1 or attempts % 15 == 0:
                log.warning("dependencies not ready (attempt %s): %s", attempts, e)
            stopping.wait(READINESS_RETRY_SECONDS)
            continue
        for name in ("", SERVICE_NAME):
            health_servicer.set(name, health_pb2.HealthCheckResponse.SERVING)
        log.info("dependencies ready, health SERVING")
        return


def main() -> int:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    settings = get_settings()
    if not settings.internal_token:
        log.error("INTERNAL_TOKEN is required")
        return 2

    tracing = setup_tracing(settings.otel_exporter_otlp_endpoint, settings.otel_service_name)
    start_metrics_server(settings.agent_metrics_port)
    container = build_container(settings)
    server, health_servicer, port = build_server(container, settings, tracing=tracing)

    stopping = threading.Event()

    def on_signal(signum, _frame):
        log.info("received %s, shutting down", signal.Signals(signum).name)
        stopping.set()

    signal.signal(signal.SIGTERM, on_signal)
    signal.signal(signal.SIGINT, on_signal)

    server.start()
    log.info("agent gRPC server listening on :%s (workers=%s, max concurrent rpcs=%s)",
             port, settings.agent_grpc_max_workers, settings.grpc_max_concurrent_rpcs)
    threading.Thread(target=mark_serving_when_ready, args=(container, health_servicer, stopping),
                     name="readiness", daemon=True).start()
    if settings.agent_evaluation_enabled:
        threading.Thread(target=run_offline_evaluation,
                         args=(container.agent_loop, container.evaluation, container.telemetry),
                         name="offline-evaluation", daemon=True).start()

    while not stopping.wait(1.0):
        pass
    health_servicer.enter_graceful_shutdown()
    server.stop(settings.agent_grpc_shutdown_grace_seconds).wait()
    shutdown_tracing()
    log.info("agent gRPC server stopped")
    return 0


if __name__ == "__main__":
    sys.exit(main())
