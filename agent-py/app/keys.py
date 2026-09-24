"""Task identity keys shared with the gateway (contract §4); both services must produce identical digests."""
import hashlib
import re

from .errors import InvalidArgumentError
from .textutil import is_blank, trim
from .models.dto import AnalysisMode

_MD5 = re.compile(r"[a-fA-F0-9]{32}")


def normalize_content_hash(media_id: int, content_hash: str | None) -> str:
    if content_hash is not None and _MD5.fullmatch(content_hash):
        return content_hash.lower()
    return f"media-{media_id}"


def _sha256(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def goal_digest(goal: str | None, mode: AnalysisMode | None = None) -> str:
    if mode is None or mode == AnalysisMode.GENERAL:
        if goal is None or is_blank(goal):
            raise InvalidArgumentError("analysis goal is required")
        return _sha256(trim(goal))
    if goal is None or is_blank(goal):
        raise InvalidArgumentError("analysis goal is required")
    # U+241F 不可见单元分隔符,避免"模式名+目标"与真实目标文本碰撞
    return _sha256(mode.name + "␟" + trim(goal))


def context_owner(content_hash: str) -> str:
    return "analysis:context-owner:" + content_hash
