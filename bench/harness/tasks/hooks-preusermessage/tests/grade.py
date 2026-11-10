import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []


def prompts(trial):
    """The system prompt of every turn, oldest first."""
    return [
        row["effective_prompt"]
        for row in trial.query(
            "SELECT p.effective_prompt FROM turns t "
            "JOIN prompt_snapshots p ON p.hash = t.prompt_snapshot_hash "
            "ORDER BY t.started_at"
        )
    ]


def rule_blocks(trial):
    """Scoped rule blocks that reached the model after a tool result."""
    return [
        row["content"]
        for row in trial.query(
            "SELECT content FROM messages "
            "WHERE role = 'user' AND content LIKE '<rule name=%'"
        )
    ]


OUT = pathlib.Path("/app/out.txt")
lines = [line.strip() for line in OUT.read_text().splitlines() if line.strip()] if OUT.is_file() else []
if lines != ["ticket: T-4821", "done"]:
    failures.append(f"out.txt holds {lines}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    seen = prompts(trial)
    if not seen or not all("ticket: T-4821" in prompt for prompt in seen):
        failures.append("the hook's text was not in the system prompt")
    steps = trial.query(
        "SELECT state FROM execution_activities WHERE step_id = 'opening:preUserMessage'"
    )
    if not steps:
        failures.append("no preUserMessage checkpoint was recorded")
    elif any(step["state"] != "completed" for step in steps):
        failures.append("the preUserMessage checkpoint did not complete")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
