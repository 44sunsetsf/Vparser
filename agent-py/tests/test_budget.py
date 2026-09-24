import time

import pytest

from app.agent import budget
from app.errors import DeadlineExceededError


def test_rejects_work_after_deadline():
    with budget.open_budget(1):
        time.sleep(0.005)
        with pytest.raises(DeadlineExceededError):
            budget.remaining_millis()
    budget.remaining_millis()  # 作用域结束后不再抛


def test_nested_scope_keeps_earlier_deadline():
    with budget.open_budget(60_000):
        outer = budget.remaining_millis()
        with budget.open_budget(600_000):
            assert budget.remaining_millis() <= outer
        assert budget.remaining_millis() <= 60_000


def test_check_prefixes_stage():
    with budget.open_budget(1):
        time.sleep(0.005)
        with pytest.raises(DeadlineExceededError, match="Planner 后终止"):
            budget.check("Planner")
