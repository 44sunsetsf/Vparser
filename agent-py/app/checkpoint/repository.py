"""AgentCheckpointRepository: MySQL is the source of truth, a Redis hash is the read-through cache
(contract §6). Writes go to the DB first; the cache is refreshed only after the transaction commits."""
import logging
from contextlib import contextmanager
from contextvars import ContextVar
from typing import Any, TypeVar

from pydantic import TypeAdapter
from pydantic_core import to_json
from sqlalchemy import text
from sqlalchemy.engine import Connection, Engine

from ..errors import AgentError
from ..models.dto import TaskStage

log = logging.getLogger("agent.checkpoint")

CACHE_TTL_SECONDS = 7 * 24 * 3600
T = TypeVar("T")

SQL_FIND_PAYLOAD = "SELECT payload FROM agent_checkpoints WHERE media_id = :media_id AND checkpoint_key = :checkpoint_key"
SQL_FIND_STAGE = "SELECT stage FROM agent_checkpoints WHERE media_id = :media_id AND checkpoint_key = :checkpoint_key"
SQL_UPSERT = (
    "INSERT INTO agent_checkpoints(media_id, checkpoint_key, stage, payload) "
    "VALUES(:media_id, :checkpoint_key, :stage, :payload) "
    "ON DUPLICATE KEY UPDATE stage = VALUES(stage), payload = VALUES(payload), updated_at = CURRENT_TIMESTAMP(3)"
)
SQL_DELETE_BY_PREFIX = (
    "DELETE FROM agent_checkpoints WHERE media_id = :media_id AND checkpoint_key LIKE CONCAT(:prefix, '%')")
SQL_DELETE = "DELETE FROM agent_checkpoints WHERE media_id = :media_id AND checkpoint_key = :checkpoint_key"
SQL_DELETE_BY_MEDIA = "DELETE FROM agent_checkpoints WHERE media_id = :media_id"


class _Tx:
    def __init__(self, conn: Connection) -> None:
        self.conn = conn
        self.after_commit: list = []


_current_tx: ContextVar[_Tx | None] = ContextVar("agent_checkpoint_tx", default=None)


class AgentCheckpointRepository:
    def __init__(self, redis_client, engine: Engine) -> None:
        self._redis = redis_client
        self._engine = engine
        self._adapters: dict[Any, TypeAdapter] = {}

    # ---- Transactions: nested calls join the outer one; after-commit hooks run only on success ----
    @contextmanager
    def transaction(self):
        if _current_tx.get() is not None:
            yield
            return
        with self._engine.begin() as conn:
            tx = _Tx(conn)
            token = _current_tx.set(tx)
            try:
                yield
            finally:
                _current_tx.reset(token)
        for action in tx.after_commit:  # 仅提交成功后执行
            action()

    def _after_commit(self, action) -> None:
        tx = _current_tx.get()
        if tx is None:
            action()
        else:
            tx.after_commit.append(action)

    @contextmanager
    def _conn(self):
        tx = _current_tx.get()
        if tx is not None:
            yield tx.conn
        else:
            with self._engine.begin() as conn:
                yield conn

    # ---- SQL ----
    def _find_payload(self, media_id: int, key: str) -> str | None:
        with self._conn() as c:
            return c.execute(text(SQL_FIND_PAYLOAD), {"media_id": media_id, "checkpoint_key": key}).scalar()

    def _find_stage(self, media_id: int, key: str) -> str | None:
        with self._conn() as c:
            return c.execute(text(SQL_FIND_STAGE), {"media_id": media_id, "checkpoint_key": key}).scalar()

    def _upsert(self, media_id: int, key: str, stage: str, payload: str | None) -> None:
        with self._conn() as c:
            c.execute(text(SQL_UPSERT), {"media_id": media_id, "checkpoint_key": key,
                                         "stage": stage, "payload": payload})

    # ---- 读 ----
    def _adapter(self, type_) -> TypeAdapter:
        adapter = self._adapters.get(type_)
        if adapter is None:
            adapter = self._adapters[type_] = TypeAdapter(type_)
        return adapter

    def read(self, media_id: int, checkpoint_name: str, redis_key: str, field: str, type_) -> Any:
        adapter = self._adapter(type_)
        try:
            cached = self._redis.hget(redis_key, field)
            if cached is not None:
                return adapter.validate_json(cached)
        except Exception as e:
            log.warning("agent_checkpoint_cache_read_failed key=%s field=%s", redis_key, field, exc_info=True)
            self._evict_fields(redis_key, field)
        try:
            payload = self._find_payload(media_id, checkpoint_name)
            if payload is None:
                return None
            value = adapter.validate_json(payload)
            self._cache_field(redis_key, field, payload, self._find_stage(media_id, checkpoint_name))
            return value
        except Exception as e:
            raise AgentError(f"读取 Agent Checkpoint 失败: {checkpoint_name}") from e

    def read_stage(self, media_id: int, checkpoint_name: str, redis_key: str) -> TaskStage | None:
        try:
            cached = self._redis.hget(redis_key, "stage")
            if cached is not None:
                stage = TaskStage.from_(cached)
                if stage is not None:
                    return stage
                self._redis.hdel(redis_key, "stage")
        except Exception:
            log.warning("agent_checkpoint_stage_cache_read_failed mediaId=%s", media_id, exc_info=True)
        persisted = self._find_stage(media_id, checkpoint_name)
        if persisted is not None:
            self._cache_stage(redis_key, persisted)
        return TaskStage.from_(persisted)

    # ---- 写 ----
    @staticmethod
    def _serialize(value: Any) -> str:
        return to_json(value, by_alias=True).decode("utf-8")

    def write(self, media_id: int, checkpoint_name: str, stage_checkpoint_name: str, redis_key: str,
              field: str, stage: TaskStage, value: Any) -> None:
        try:
            payload = self._serialize(value)
            with self.transaction():
                # 数据库是恢复真源,缓存失效只影响速度,不影响用户继续任务。
                self._upsert(media_id, checkpoint_name, stage.name, payload)
                self._upsert(media_id, stage_checkpoint_name, stage.name, None)
                self._after_commit(lambda: self._cache_field(redis_key, field, payload, stage.name))
        except Exception as e:
            raise AgentError("保存 Agent Checkpoint 失败") from e

    def write_standalone(self, media_id: int, checkpoint_name: str, redis_key: str, field: str,
                         stage: TaskStage, value: Any) -> None:
        try:
            payload = self._serialize(value)
            with self.transaction():
                self._upsert(media_id, checkpoint_name, stage.name, payload)
                self._after_commit(lambda: self._cache_field(redis_key, field, payload, stage.name))
        except Exception as e:
            raise AgentError("保存 Agent Checkpoint 失败") from e

    def write_stage(self, media_id: int, checkpoint_name: str, redis_key: str, stage: TaskStage) -> None:
        self._upsert(media_id, checkpoint_name, stage.name, None)
        self._cache_stage(redis_key, stage.name)

    def delete_by_prefix(self, media_id: int, prefix: str) -> None:
        with self._conn() as c:
            c.execute(text(SQL_DELETE_BY_PREFIX), {"media_id": media_id, "prefix": prefix})

    def delete(self, media_id: int, checkpoint_name: str, redis_key: str) -> None:
        with self.transaction():
            with self._conn() as c:
                c.execute(text(SQL_DELETE), {"media_id": media_id, "checkpoint_key": checkpoint_name})

            def drop_cache():
                try:
                    self._redis.delete(redis_key)
                except Exception:
                    log.warning("agent_checkpoint_cache_delete_failed key=%s", redis_key, exc_info=True)
            self._after_commit(drop_cache)

    def delete_by_media_id(self, media_id: int) -> None:
        with self._conn() as c:
            c.execute(text(SQL_DELETE_BY_MEDIA), {"media_id": media_id})

    # ---- Redis 缓存 ----
    def _cache_field(self, key: str, field: str, payload: str, stage: str | None) -> None:
        try:
            self._redis.hset(key, field, payload)
            if stage is not None:
                self._redis.hset(key, "stage", stage)
            self._redis.expire(key, CACHE_TTL_SECONDS)
        except Exception:
            log.warning("agent_checkpoint_cache_write_failed key=%s field=%s", key, field, exc_info=True)
            self._evict_fields(key, field, "stage")

    def _cache_stage(self, key: str, stage: str) -> None:
        try:
            self._redis.hset(key, "stage", stage)
            self._redis.expire(key, CACHE_TTL_SECONDS)
        except Exception:
            log.warning("agent_checkpoint_stage_cache_write_failed key=%s", key, exc_info=True)
            self._evict_fields(key, "stage")

    def _evict_fields(self, key: str, *fields: str) -> None:
        try:
            self._redis.hdel(key, *fields)
        except Exception:
            pass
