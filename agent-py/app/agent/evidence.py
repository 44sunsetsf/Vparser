"""EvidenceVerificationService: every piece of evidence must be traceable to the raw ASR / OCR text."""
import unicodedata

from ..models.dto import Evidence, VideoContext, VideoSegment

_ASCII_WS = " \t\n\x0b\f\r"


def _normalize(value: str | None) -> str:
    """Lower-case and drop punctuation, symbols and ASCII whitespace, so matching ignores formatting."""
    if value is None:
        return ""
    return "".join(ch for ch in value.lower()
                   if ch not in _ASCII_WS and unicodedata.category(ch)[0] not in ("P", "S"))


def _contains_timestamp(segment: VideoSegment, timestamp_ms: int) -> bool:
    return segment.start_ms <= timestamp_ms < segment.end_ms


def _source_text(segment: VideoSegment, source: str) -> str:
    if "ASR" in source and "OCR" in source:
        return segment.transcript + " " + " ".join(segment.ocr_texts)
    if "ASR" in source:
        return segment.transcript
    return " ".join(segment.ocr_texts)


def _text_matches(evidence: str, candidate: str) -> bool:
    e, c = _normalize(evidence), _normalize(candidate)
    return bool(e) and bool(c) and e in c


class EvidenceVerificationService:
    def timestamp_covered(self, context: VideoContext | None, evidence: Evidence | None) -> bool:
        return (context is not None and evidence is not None
                and any(_contains_timestamp(s, evidence.timestamp_ms) for s in context.segments))

    def supported(self, context: VideoContext | None, evidence: Evidence | None) -> bool:
        from ..textutil import is_blank
        if context is None or evidence is None or is_blank(evidence.content):
            return False
        source = evidence.source.upper()
        if "ASR" not in source and "OCR" not in source:
            return False
        return any(_text_matches(evidence.content, _source_text(s, source))
                   for s in context.segments if _contains_timestamp(s, evidence.timestamp_ms))

    def supports_claim(self, context: VideoContext | None, claim: str | None, evidence: Evidence | None) -> bool:
        return (evidence is not None and _normalize(claim) != ""
                and _normalize(claim) == _normalize(evidence.claim)
                and self.supported(context, evidence))
