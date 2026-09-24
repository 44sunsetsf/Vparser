"""Snapshot test for every prompt fragment in app/llm/prompts.py.

Prompt text is behaviour: an accidental edit silently changes model output. Each public string constant
(and the rendered mode suffixes) is compared with tests/snapshots/prompts/<NAME>.txt. After a deliberate
prompt change, regenerate and review the diff:

    UPDATE_SNAPSHOTS=1 uv run pytest tests/test_prompt_snapshots.py
"""
import os
from pathlib import Path

import pytest

from app.agent.modes import ModeRegistry
from app.llm import prompts as P
from app.models.dto import AnalysisMode

SNAPSHOT_DIR = Path(__file__).parent / "snapshots" / "prompts"
UPDATE = os.environ.get("UPDATE_SNAPSHOTS") == "1"


def _prompts() -> dict[str, str]:
    found = {name: value for name, value in vars(P).items() if name.isupper() and isinstance(value, str)}
    modes = ModeRegistry()
    for mode in AnalysisMode:
        profile = modes.of(mode)
        found[f"rendered_plan_suffix_{mode.value}"] = P.mode_suffix(
            "本次分析模式的额外拆解要求：", profile.plan_instruction)
        found[f"rendered_execute_suffix_{mode.value}"] = P.execute_suffix(profile.execute_instruction)
        found[f"rendered_critic_suffix_{mode.value}"] = P.mode_suffix(
            "本次审查模式的额外校验要求：", profile.critic_instruction)
    return found


PROMPTS = _prompts()


@pytest.mark.parametrize("name", sorted(PROMPTS))
def test_prompt_matches_snapshot(name):
    path = SNAPSHOT_DIR / f"{name}.txt"
    if UPDATE:
        SNAPSHOT_DIR.mkdir(parents=True, exist_ok=True)
        path.write_text(PROMPTS[name], encoding="utf-8", newline="")
    assert path.exists(), f"missing snapshot {path.name}; run with UPDATE_SNAPSHOTS=1 and review it"
    assert PROMPTS[name] == path.read_text(encoding="utf-8"), (
        f"prompt {name} changed; if intended, run with UPDATE_SNAPSHOTS=1 and review the diff")


def test_no_orphan_snapshots():
    if UPDATE:
        for path in SNAPSHOT_DIR.glob("*.txt"):
            if path.stem not in PROMPTS:
                path.unlink()
    orphans = sorted(p.stem for p in SNAPSHOT_DIR.glob("*.txt") if p.stem not in PROMPTS)
    assert not orphans, f"snapshots without a prompt: {orphans}"
