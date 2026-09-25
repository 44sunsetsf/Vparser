"""Composition root: wires every service once per process. Construction never touches the network;
connections are opened lazily on first use."""
import logging
from dataclasses import dataclass
from datetime import datetime
from functools import lru_cache
from zoneinfo import ZoneInfo

import redis
from sqlalchemy import create_engine
from sqlalchemy.engine import URL, Engine

from .agent.evaluation import AgentEvaluationService
from .agent.evidence import EvidenceVerificationService
from .agent.followup import FollowUpService
from .agent.loop import AgentLoopService
from .agent.modes import ModeRegistry, ModeRouter
from .agent.service import AgentService
from .billing import SpendLedger
from .checkpoint.repository import AgentCheckpointRepository
from .checkpoint.service import AgentCheckpointService
from .config import Settings, get_settings
from .events import TaskEventPublisher
from .llm.deepseek import DeepSeekClient
from .llm.embedding import EmbeddingClient
from .retrieval.chunking import VideoChunkingService
from .retrieval.evidence_retrieval import VideoEvidenceRetrievalService
from .retrieval.long_context import LongVideoContextService
from .retrieval.qdrant import QdrantVectorStore
from .telemetry import AgentTelemetry

log = logging.getLogger("agent.container")


def build_redis(settings: Settings) -> redis.Redis:
    return redis.Redis(host=settings.redis_host, port=settings.redis_port, db=settings.redis_database,
                       password=settings.redis_password or None, decode_responses=True,
                       socket_timeout=3, socket_connect_timeout=3)


def _tz_offset(name: str | None) -> str | None:
    """IANA zone -> MySQL session time_zone offset (Asia/Shanghai -> +08:00); None if unknown."""
    if not name:
        return None
    try:
        offset = datetime.now(ZoneInfo(name)).utcoffset()
    except Exception:
        return None
    if offset is None:
        return None
    minutes = int(offset.total_seconds() // 60)
    sign = "+" if minutes >= 0 else "-"
    minutes = abs(minutes)
    return f"{sign}{minutes // 60:02d}:{minutes % 60:02d}"


def build_engine(settings: Settings) -> Engine:
    url = URL.create("mysql+pymysql", username=settings.db_username or None,
                     password=settings.db_password or None, host=settings.db_host, port=settings.db_port,
                     database=settings.db_name, query={"charset": "utf8mb4"})
    connect_args = {"connect_timeout": 3}
    offset = _tz_offset(settings.db_timezone)
    if offset:
        connect_args["init_command"] = f"SET time_zone = '{offset}'"
    return create_engine(url, pool_size=settings.db_pool_max_size, max_overflow=0, pool_timeout=3,
                         pool_pre_ping=True, pool_recycle=1800, connect_args=connect_args)


@dataclass
class Container:
    settings: Settings
    redis: redis.Redis
    engine: Engine
    telemetry: AgentTelemetry
    checkpoint: AgentCheckpointService
    vector_store: QdrantVectorStore
    deepseek: DeepSeekClient
    evaluation: AgentEvaluationService
    agent_loop: AgentLoopService
    agent: AgentService
    follow_up: FollowUpService
    modes: ModeRegistry
    router: ModeRouter


def build_container(settings: Settings | None = None) -> Container:
    s = settings or get_settings()
    redis_client = build_redis(s)
    engine = build_engine(s)
    telemetry = AgentTelemetry(redis_client)
    repository = AgentCheckpointRepository(redis_client, engine)
    checkpoint = AgentCheckpointService(redis_client, repository)
    deepseek = DeepSeekClient(s, telemetry, ledger=SpendLedger(redis_client))
    embedding = EmbeddingClient(s)
    vector_store = QdrantVectorStore(s)
    retrieval = VideoEvidenceRetrievalService(deepseek, embedding, vector_store, telemetry)
    chunking = VideoChunkingService(deepseek, embedding, telemetry)
    long_context = LongVideoContextService(telemetry, checkpoint, chunking, retrieval)
    evidence = EvidenceVerificationService()
    events = TaskEventPublisher(redis_client)
    agent_loop = AgentLoopService(deepseek, long_context, checkpoint, telemetry, evidence, events,
                                  s.agent_max_rounds, s.agent_max_duration_ms,
                                  s.agent_max_estimated_tokens, s.agent_max_estimated_cost)
    modes = ModeRegistry()
    agent = AgentService(agent_loop, long_context, checkpoint, telemetry, modes)
    follow_up = FollowUpService(deepseek, long_context, checkpoint, telemetry)
    return Container(s, redis_client, engine, telemetry, checkpoint, vector_store, deepseek,
                     AgentEvaluationService(checkpoint, evidence), agent_loop, agent, follow_up, modes,
                     ModeRouter(deepseek))


@lru_cache
def get_container() -> Container:
    return build_container()
