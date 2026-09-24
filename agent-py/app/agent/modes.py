"""Analysis modes: per-mode prompt instructions (ModeRegistry) and LLM intent routing (ModeRouter).
Rate limiting of routing requests is the gateway's job."""
import logging
from dataclasses import dataclass, field

from ..textutil import is_blank, trim
from ..llm.deepseek import DeepSeekClient
from ..models.dto import AnalysisMode, RouteDecision

log = logging.getLogger("agent.mode")


@dataclass(frozen=True)
class ModeProfile:
    mode: AnalysisMode
    display_name: str
    plan_instruction: str = ""
    execute_instruction: str = ""
    critic_instruction: str = ""
    required_section_keys: tuple[str, ...] = field(default_factory=tuple)

    def __post_init__(self) -> None:
        object.__setattr__(self, "required_section_keys", tuple(dict.fromkeys(self.required_section_keys)))


class ModeRegistry:
    def __init__(self) -> None:
        self._profiles: dict[AnalysisMode, ModeProfile] = {}
        # 默认模式:空指令 = 完全等于现有行为,作为兜底
        self._register(ModeProfile(AnalysisMode.GENERAL, "通用分析", "", "", "", ()))

        # 学习模式:强化知识结构化
        self._register(ModeProfile(
            AnalysisMode.LEARNING, "学习复习",
            "按知识主题而非时间顺序拆解任务,覆盖核心概念、原理与易混点。",
            "在结论之外,额外产出以下产物段落:"
            "key=outline 知识点大纲、key=keypoints 重点难点、"
            "key=quiz 自测题(每题附答案)、key=pitfalls 易错点。",
            "额外检查:知识点是否成体系、讲解是否有跳步、自测题是否覆盖核心概念。",
            ("outline", "keypoints", "quiz", "pitfalls")))

        # 审查模式:强化 Critic 质疑
        self._register(ModeProfile(
            AnalysisMode.REVIEW, "内容审查",
            "把目标拆成对每个主要论点的可验证审查项。",
            "在结论之外,额外产出以下产物段落:"
            "key=fallacies 逻辑漏洞、key=exaggerations 夸大表述、"
            "key=omissions 遗漏点、key=doubtful 存疑结论(附理由)。",
            "以更严格的门槛质疑:论据是否充分、有无偷换概念、结论是否被证据支持;"
            "证据不足时必须判定不通过。",
            ("fallacies", "exaggerations", "omissions", "doubtful")))

        # 创作模式:强化时间戳爆点定位
        self._register(ModeProfile(
            AnalysisMode.CREATION, "内容创作",
            "围绕'可发布资产'拆解:定位爆点、可切片段落与传播钩子。",
            "在结论之外,额外产出以下产物段落:"
            "key=highlights 爆点片段(每条含起止时间戳)、key=titles 备选标题、"
            "key=intro 简介文案、key=script 口播脚本要点。",
            "检查每个爆点是否有真实时间戳支撑、文案是否贴合视频实际内容,不得虚构。",
            ("highlights", "titles", "intro", "script")))

        # 启动自检:每个 AnalysisMode 都必须注册 Profile,否则读写两端 key 不对称。
        for value in AnalysisMode:
            if value not in self._profiles:
                raise RuntimeError(f"AnalysisMode 未注册 ModeProfile: {value}")

    def _register(self, profile: ModeProfile) -> None:
        self._profiles[profile.mode] = profile

    def of(self, mode: AnalysisMode | None) -> ModeProfile:
        return self._profiles.get(mode, self._profiles[AnalysisMode.GENERAL])


_FALLBACK_REASON = {
    AnalysisMode.LEARNING: "目标偏向知识梳理与复习,已选学习模式",
    AnalysisMode.REVIEW: "目标偏向查错与观点核验,已选审查模式",
    AnalysisMode.CREATION: "目标偏向内容再创作,已选创作模式",
    AnalysisMode.GENERAL: "目标较为通用,已选通用模式",
}


class ModeRouter:
    def __init__(self, deepseek: DeepSeekClient) -> None:
        self._deepseek = deepseek

    def route(self, goal: str | None) -> RouteDecision:
        """任何异常/空目标/无法识别的返回都回退 GENERAL,永不抛错。"""
        if goal is None or is_blank(goal):
            return RouteDecision(mode=AnalysisMode.GENERAL, reason="未提供分析目标,已按通用模式分析")
        try:
            classification = self._deepseek.classify_mode(trim(goal))
            mode = AnalysisMode.from_nullable(None if classification is None else classification.mode)
            reason = self._pick_reason(classification, mode)
            return RouteDecision(mode=mode, reason=reason)
        except Exception:
            log.warning("自动意图路由失败,回退 GENERAL。goalLength=%s", len(goal), exc_info=True)
            return RouteDecision(mode=AnalysisMode.GENERAL, reason="意图识别暂不可用,已按通用模式分析")

    @staticmethod
    def _pick_reason(classification, mode: AnalysisMode) -> str:
        if (classification is not None and classification.reason is not None
                and not is_blank(classification.reason)):
            return trim(classification.reason)
        return _FALLBACK_REASON[mode]
