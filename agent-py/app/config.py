"""Service configuration, read from environment variables (docs/architecture.md §10)."""
from functools import lru_cache

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(case_sensitive=False, extra="ignore")

    # gRPC server
    agent_grpc_port: int = 9091
    agent_grpc_max_workers: int = 16
    # 0 = same as max workers: requests beyond the pool are rejected immediately instead of queueing.
    agent_grpc_max_concurrent_rpcs: int = 0
    agent_grpc_shutdown_grace_seconds: float = 30.0
    agent_metrics_port: int = 9464
    internal_token: str = ""

    # Observability
    otel_exporter_otlp_endpoint: str = ""
    otel_service_name: str = "agent-py"

    # MySQL
    db_host: str = "localhost"
    db_port: int = 3307
    db_name: str = "media_db"
    db_username: str = ""
    db_password: str = ""
    db_timezone: str = "Asia/Shanghai"
    db_pool_max_size: int = 10

    # Model provider (OpenAI-compatible)
    siliconflow_api_key: str = ""
    siliconflow_base_url: str = "https://api.siliconflow.cn/v1"
    # Measured on the same structured-notes task (2026-09): DeepSeek-V3.2 took 20–47s per call,
    # Qwen3.6-35B-A3B with thinking off 3.5–5.4s, with valid JSON and verbatim quotes (5/5).
    llm_model: str = "Qwen/Qwen3.6-35B-A3B"
    llm_timeout_seconds: int = 300
    # Hybrid-reasoning models (e.g. Qwen3.x) think before answering unless told not to, which costs
    # 10–25s per call. False turns it off; None sends nothing and keeps the provider default.
    llm_enable_thinking: bool | None = False
    llm_input_price_per_million: float = 0
    llm_output_price_per_million: float = 0
    embedding_model: str = "BAAI/bge-m3"

    # Agent budgets
    agent_max_rounds: int = 2
    agent_max_duration_ms: int = 120000
    agent_max_estimated_tokens: int = 50000
    agent_max_estimated_cost: float = 0
    agent_evaluation_enabled: bool = False

    # Vector store
    qdrant_enabled: bool = True
    qdrant_url: str = "http://localhost:6333"
    qdrant_api_key: str = ""
    qdrant_collection: str = "video_chunks"

    # Redis
    redis_host: str = "localhost"
    redis_port: int = 6379
    redis_database: int = 0
    redis_password: str = ""

    @property
    def grpc_max_concurrent_rpcs(self) -> int:
        return self.agent_grpc_max_concurrent_rpcs or self.agent_grpc_max_workers


@lru_cache
def get_settings() -> Settings:
    return Settings()
