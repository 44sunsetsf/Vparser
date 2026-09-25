"""Output language of a report, taken from the language the user wrote the goal (or question) in.

Chinese is the default and needs nothing extra: the prompts are written in Chinese, so for a Chinese goal
``prompt_suffix`` is empty and the prompts stay byte-for-byte what tests/test_prompt_snapshots.py pins.
For an English or Swedish goal one instruction is appended, and the Markdown headings follow suit.
Evidence quotes are never translated: the evidence check compares them with the original ASR/OCR text.
"""
import re

ZH, EN, SV = "zh", "en", "sv"

_CJK = re.compile(r"[㐀-䶿一-鿿豈-﫿]")
_LATIN = re.compile(r"[A-Za-zÀ-ÿ]")
_SWEDISH_LETTERS = re.compile(r"[åäöÅÄÖ]")
_SWEDISH_WORDS = {"och", "att", "är", "för", "med", "som", "det", "på", "inte", "jag", "vad", "hur", "av", "till",
                  "om", "en", "ett", "den", "de", "vi", "ge", "sammanfatta", "videon", "viktigaste"}


def detect(text: str | None) -> str:
    """zh when the text is mostly Chinese; sv for Swedish; otherwise en. Empty text keeps the Chinese default."""
    if not text or not text.strip():
        return ZH
    cjk = len(_CJK.findall(text))
    latin = len(_LATIN.findall(text))
    # a Chinese goal that names English terms (e.g. "AI Agent") is still Chinese
    if cjk and cjk * 3 >= latin:
        return ZH
    words = re.findall(r"[a-zåäö]+", text.lower())
    # too little text to tell (ids, placeholders, a single keyword): keep the default
    if len(words) < 3:
        return ZH
    if _SWEDISH_LETTERS.search(text) or sum(w in _SWEDISH_WORDS for w in words) >= 2:
        return SV
    return EN


_NAMES = {EN: "English", SV: "Swedish (svenska)"}


def prompt_suffix(lang: str) -> str:
    """Instruction appended to a prompt so the model writes in the user's language; empty for Chinese."""
    name = _NAMES.get(lang)
    if name is None:
        return ""
    return ("\n\nOutput language: write every natural-language value in " + name
            + " — for example title, understoodGoal, tasks, conclusions, evidence.claim, suggestions, feedback, "
            + "reason, and the title and items of any sections. Do not translate evidence.content: copy it "
            + "exactly as it appears in the original ASR/OCR text. Keep JSON keys, source values and "
            + "timestamps unchanged.")


def answer_suffix(lang: str) -> str:
    """Instruction for free-form Markdown answers (follow-up questions); empty for Chinese."""
    name = _NAMES.get(lang)
    if name is None:
        return ""
    return ("\n\nAnswer in " + name + ". Quotes from the video may stay in their original language; "
            + "keep the [mm:ss] timestamp format.")


# Markdown headings and fixed phrases of a finished report
LABELS = {
    ZH: {"untitled": "未命名分析", "conclusions": "核心结论", "evidence": "视频证据", "suggestions": "建议",
         "note": "结果提示", "warning": "分析已完成，但部分结论未通过 Critic 校验，请结合时间戳证据人工核验。",
         "colon": "："},
    EN: {"untitled": "Untitled analysis", "conclusions": "Key conclusions", "evidence": "Evidence from the video",
         "suggestions": "Recommendations", "note": "Note",
         "warning": "The analysis finished, but some conclusions did not pass the Critic check. "
                    "Verify them against the timestamped evidence.",
         "colon": ": "},
    SV: {"untitled": "Namnlös analys", "conclusions": "Viktigaste slutsatser", "evidence": "Belägg i videon",
         "suggestions": "Rekommendationer", "note": "Obs",
         "warning": "Analysen är klar, men vissa slutsatser klarade inte Critic-kontrollen. "
                    "Kontrollera dem mot de tidsstämplade beläggen.",
         "colon": ": "},
}


def labels(lang: str) -> dict[str, str]:
    return LABELS.get(lang, LABELS[ZH])
