"""EmbeddingClient: POST {base}/embeddings on an OpenAI-compatible endpoint."""
import json
import re

import httpx

from ..errors import AgentError
from ..textutil import is_blank


class EmbeddingClient:
    def __init__(self, settings, client: httpx.Client | None = None) -> None:
        self._api_key = settings.siliconflow_api_key
        self._base_url = re.sub(r"/+$", "", settings.siliconflow_base_url)
        self._model = settings.embedding_model
        self._client = client or httpx.Client(timeout=httpx.Timeout(120.0, connect=30.0))

    def embed(self, text: str | None) -> list[float]:
        if text is None or is_blank(text):
            return []
        try:
            body = json.dumps({"model": self._model, "input": text}, ensure_ascii=False, separators=(",", ":"))
            response = self._client.post(
                self._base_url + "/embeddings",
                headers={"Authorization": "Bearer " + self._api_key,
                         "Content-Type": "application/json; charset=utf-8"},
                content=body.encode("utf-8"))
            if not response.is_success:
                raise AgentError(f"Embedding API failed: {response.status_code}")
            data = response.json().get("data")
            if not data:
                raise AgentError("Embedding data is empty")
            values = data[0].get("embedding")
            if not values:
                raise AgentError("Embedding vector is empty")
            return [float(v) for v in values]
        except Exception as e:
            raise AgentError("Embedding 生成失败") from e
