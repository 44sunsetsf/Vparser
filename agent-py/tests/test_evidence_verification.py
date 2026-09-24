from app.agent.evidence import EvidenceVerificationService
from app.models.dto import Evidence, VideoContext, VideoSegment

service = EvidenceVerificationService()
context = VideoContext(
    source="lesson.mp4", user_goal="总结课程",
    segments=[VideoSegment(
        start_ms=120_000, end_ms=180_000, transcript="接下来讲解二叉树的前序遍历",
        ocr_texts=["前序遍历：根节点、左子树、右子树"], evidence_frames=["frame_000125.jpg"])])


def test_accepts_verbatim_evidence_at_the_declared_timestamp():
    evidence = Evidence(timestamp_ms=125_000, source="OCR", content="根节点、左子树、右子树", claim="前序遍历顺序")
    assert service.supported(context, evidence)
    assert service.supports_claim(context, "前序遍历顺序", evidence)


def test_rejects_text_that_only_looks_similar_to_the_source():
    evidence = Evidence(timestamp_ms=125_000, source="OCR", content="根节点左子树不存在，因此应跳过", claim="前序遍历顺序")
    assert not service.supported(context, evidence)


def test_timestamp_outside_segment_is_unsupported():
    evidence = Evidence(timestamp_ms=180_000, source="ASR", content="二叉树", claim="x")
    assert not service.supported(context, evidence)
    assert not service.timestamp_covered(context, evidence)
