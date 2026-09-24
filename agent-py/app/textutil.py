"""String helpers shared by keys and DTO normalisation.

``trim`` implements the cross-service goalDigest rule (docs/architecture.md §4): strip only leading and
trailing code points <= U+0020. It deliberately differs from ``str.strip()``, which would also remove
Unicode whitespace such as U+3000 and so produce a different digest for the same goal.
"""
import unicodedata

# Space separators that are *not* treated as blank (no-break spaces keep text visually non-empty).
_NON_BREAKING = {"\u00a0", "\u2007", "\u202f"}


def trim(value: str) -> str:
    start, end = 0, len(value)
    while start < end and value[start] <= " ":
        start += 1
    while end > start and value[end - 1] <= " ":
        end -= 1
    return value[start:end]


def _is_ws(ch: str) -> bool:
    if ch in _NON_BREAKING:
        return False
    if ch in "\t\n\x0b\x0c\r\x1c\x1d\x1e\x1f":
        return True
    return unicodedata.category(ch) in ("Zs", "Zl", "Zp")


def is_blank(value: str | None) -> bool:
    """True for None or text made only of whitespace (including Unicode space separators)."""
    return value is None or all(_is_ws(c) for c in value)
