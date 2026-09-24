FROM python:3.12-slim

COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

ENV UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    UV_PYTHON_DOWNLOADS=never \
    PYTHONUNBUFFERED=1 \
    AGENT_GRPC_PORT=9091 \
    AGENT_METRICS_PORT=9464

WORKDIR /srv/agent
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY app ./app

RUN useradd --system --uid 10001 --no-create-home agent
USER agent

ENV PATH="/srv/agent/.venv/bin:$PATH"
# 9091 = gRPC (internal only), 9464 = Prometheus /metrics
EXPOSE 9091 9464

HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=5 \
    CMD ["python", "-m", "app.healthcheck"]

# exec form: the server is PID 1 and receives SIGTERM directly for graceful shutdown
STOPSIGNAL SIGTERM
CMD ["python", "-m", "app.server"]
