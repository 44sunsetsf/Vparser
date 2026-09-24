"""Publishes agent-side task stage events to the Redis channel dovideo:task-events (contract §7).
The gateway owns the SSE connections and fans events out to subscribers."""
import logging

from pydantic_core import to_json

from .keys import goal_digest
from .models.dto import AnalysisMode, TaskEvent, TaskStage, TaskStatus

log = logging.getLogger("agent.events")

REDIS_CHANNEL = "dovideo:task-events"
ANALYSIS = "analysis"
TRANSCRIPTION = "transcription"


def event_key(media_id: int, type_: str, goal: str, mode: AnalysisMode) -> str:
    suffix = goal_digest(goal, mode) if type_ == ANALYSIS else "default"
    return f"{type_}:{media_id}:{suffix}"


class TaskEventPublisher:
    def __init__(self, redis_client) -> None:
        self._redis = redis_client

    def publish_analysis(self, media_id: int, goal: str, mode: AnalysisMode,
                         status: TaskStatus, stage: TaskStage) -> None:
        self._publish(event_key(media_id, ANALYSIS, goal, mode), TaskEvent.of(status, stage))

    def _publish(self, key: str, event: TaskEvent) -> None:
        try:
            payload = to_json({"key": key, "event": event}, by_alias=True).decode("utf-8")
            self._redis.publish(REDIS_CHANNEL, payload)
        except Exception:
            log.warning("task_event_redis_publish_failed key=%s", key, exc_info=True)
