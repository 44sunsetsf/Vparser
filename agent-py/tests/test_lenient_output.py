"""Models sometimes bend the output schema a little; small deviations must not fail a whole analysis."""
from app.agent.loop import MAX_PLAN_TASKS, _tidy_plan
from app.models.dto import AgentPlan, AnalysisResult


def test_object_items_become_text():
    # seen in production (LEARNING mode): self-test questions returned as objects instead of strings
    raw = ('{"title":"t","conclusions":["c"],"evidence":[],"sections":[{"key":"self_test","title":"自测题",'
           '"items":[{"question":"ETF 为什么更节税？","answer":"实物申赎"},"普通条目",3]}]}')
    result = AnalysisResult.model_validate_json(raw)
    assert result.sections[0].items == ["ETF 为什么更节税？ — 实物申赎", "普通条目", "3"]


def test_nested_lists_become_text():
    result = AnalysisResult.model_validate({"title": "t", "conclusions": [["a", "b"]], "evidence": []})
    assert result.conclusions == ["a；b"]


def test_plan_is_tidied_not_rejected():
    plan = AgentPlan(understood_goal="g", tasks=["  a ", "", "b", "c", "d", "e", "f", "x" * 800])
    tidy = _tidy_plan(plan)
    assert tidy.tasks[:5] == ["a", "b", "c", "d", "e"]
    assert len(tidy.tasks) == MAX_PLAN_TASKS
    long = _tidy_plan(AgentPlan(understood_goal="g", tasks=["x" * 800]))
    assert len(long.tasks[0]) == 500


def test_tidy_leaves_missing_plans_alone():
    assert _tidy_plan(None) is None
    assert _tidy_plan(AgentPlan(understood_goal="g", tasks=[" "])).tasks == []   # still goes to repair
