"""QdrantVectorStore: Qdrant REST API over httpx; one point per chunk, filtered by mediaId."""
import hashlib
import json
import logging
import re
import threading
import uuid
from dataclasses import dataclass

import httpx

from ..errors import InvalidArgumentError, AgentError
from ..textutil import is_blank
from ..models.dto import VideoChunk

log = logging.getLogger("agent.qdrant")


@dataclass
class VectorHit:
    start_ms: int
    end_ms: int
    score: float


def _body(obj) -> bytes:
    return json.dumps(obj, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def point_id(media_id: int, chunk: VideoChunk) -> str:
    """Deterministic point id: MD5 of "mediaId:start:end" stamped as a version-3 UUID, so re-indexing
    a chunk overwrites the same point."""
    source = f"{media_id}:{chunk.start_time}:{chunk.end_time}"
    digest = bytearray(hashlib.md5(source.encode("utf-8")).digest())
    digest[6] = (digest[6] & 0x0F) | 0x30
    digest[8] = (digest[8] & 0x3F) | 0x80
    return str(uuid.UUID(bytes=bytes(digest)))


class QdrantVectorStore:
    def __init__(self, settings, client: httpx.Client | None = None) -> None:
        if not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", settings.qdrant_collection):
            raise InvalidArgumentError("Qdrant collection name is invalid")
        self._enabled = settings.qdrant_enabled
        self._base_url = re.sub(r"/+$", "", settings.qdrant_url)
        self._api_key = settings.qdrant_api_key
        self._collection = settings.qdrant_collection
        self._collection_ready = False
        self._lock = threading.Lock()
        self._client = client or httpx.Client(timeout=httpx.Timeout(10.0, connect=3.0))

    def upsert(self, media_id: int, chunks: list[VideoChunk]) -> None:
        if not self._enabled:
            return
        vectorized = [c for c in chunks if c.embedding]
        if not vectorized:
            return
        try:
            self._ensure_collection(len(vectorized[0].embedding))
            points = [{
                "id": point_id(media_id, c),
                "vector": c.embedding,
                "payload": {"mediaId": media_id, "startMs": c.start_time, "endMs": c.end_time},
            } for c in vectorized]
            self._execute("PUT", f"{self._base_url}/collections/{self._collection}/points?wait=true",
                          {"points": points})
        except Exception as e:
            self._collection_ready = False
            raise AgentError("Qdrant 分段向量写入失败") from e

    def search(self, media_id: int, query_embedding: list[float], limit: int) -> list[VectorHit]:
        if not self._enabled or not query_embedding:
            return []
        try:
            self._ensure_collection(len(query_embedding))
            body = {
                "query": query_embedding,
                "filter": {"must": [{"key": "mediaId", "match": {"value": media_id}}]},
                "limit": limit,
                "with_payload": True,
            }
            response = self._execute(
                "POST", f"{self._base_url}/collections/{self._collection}/points/query", body)
            result = json.loads(response).get("result")
            points = result.get("points") if result else None
            if points is None:
                return []
            hits = []
            for point in points:
                payload = point.get("payload")
                if payload is None:
                    continue
                hits.append(VectorHit(int(payload.get("startMs", 0)), int(payload.get("endMs", 0)),
                                      float(point.get("score", 0.0))))
            return hits
        except Exception as e:
            self._collection_ready = False
            raise AgentError("Qdrant 语义检索失败") from e

    def delete_media(self, media_id: int) -> None:
        if not self._enabled:
            return
        try:
            body = {"filter": {"must": [{"key": "mediaId", "match": {"value": media_id}}]}}
            self._execute("POST", f"{self._base_url}/collections/{self._collection}/points/delete?wait=true", body)
        except Exception:
            log.warning("qdrant_media_cleanup_failed mediaId=%s", media_id, exc_info=True)

    def _ensure_collection(self, vector_size: int) -> None:
        if self._collection_ready:
            return
        with self._lock:
            if self._collection_ready:
                return
            try:
                response = self._client.get(f"{self._base_url}/collections/{self._collection}",
                                            headers=self._headers())
                if response.is_success:
                    self._collection_ready = True
                    return
                if response.status_code != 404:
                    raise AgentError(f"Qdrant collection lookup failed: {response.status_code}")
            except Exception as e:
                raise AgentError("Qdrant collection lookup failed") from e
            self._execute("PUT", f"{self._base_url}/collections/{self._collection}",
                          {"vectors": {"size": vector_size, "distance": "Cosine"}})
            self._collection_ready = True

    def _headers(self) -> dict[str, str]:
        headers = {}
        if not is_blank(self._api_key):
            headers["api-key"] = self._api_key
        return headers

    def _execute(self, method: str, url: str, body: dict) -> str:
        try:
            headers = self._headers()
            headers["Content-Type"] = "application/json; charset=utf-8"
            response = self._client.request(method, url, headers=headers, content=_body(body))
            if not response.is_success:
                raise AgentError(f"Qdrant API failed: {response.status_code} {response.text}")
            return response.text
        except Exception as e:
            raise AgentError("Qdrant request failed") from e
