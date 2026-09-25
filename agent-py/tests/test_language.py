from app import language as L
from app.models.dto import AnalysisResult, Evidence


def test_detect_language_of_goal():
    assert L.detect("理解视频核心内容，提炼关键结论，并给出带时间戳的证据和可执行建议") == L.ZH
    assert L.detect("梳理 AI Agent 的核心观点") == L.ZH
    assert L.detect("Understand the core content of the video and give timestamped evidence") == L.EN
    assert L.detect("Förstå videons kärninnehåll och ge tidsstämplade belägg") == L.SV
    assert L.detect("Sammanfatta videon och ge konkreta råd till mig") == L.SV
    # empty or too short to tell: keep the Chinese default so existing behaviour is unchanged
    for text in (None, "", "   ", "g", "RAG Kafka"):
        assert L.detect(text) == L.ZH


def test_prompt_suffix_is_empty_for_chinese():
    assert L.prompt_suffix(L.ZH) == ""
    assert L.answer_suffix(L.ZH) == ""
    assert "English" in L.prompt_suffix(L.EN) and "evidence.content" in L.prompt_suffix(L.EN)
    assert "Swedish" in L.prompt_suffix(L.SV)


def test_markdown_headings_follow_language():
    result = AnalysisResult(title="T", conclusions=["c"], suggestions=["s"],
                            evidence=[Evidence(timestamp_ms=65000, source="ASR", content="原文", claim="c")])
    zh = result.to_markdown()
    assert "## 核心结论" in zh and "ASR：原文" in zh
    en = result.to_markdown(L.EN)
    assert "## Key conclusions" in en and "## Recommendations" in en and "ASR: 原文" in en
    assert "## Viktigaste slutsatser" in result.to_markdown(L.SV)
