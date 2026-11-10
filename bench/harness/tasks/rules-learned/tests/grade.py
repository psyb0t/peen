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


PRICE = pathlib.Path("/app/price.txt")
if not PRICE.is_file() or PRICE.read_text().strip() != "250c":
    failures.append("price.txt does not hold exactly 250c")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    seen = prompts(trial)
    if not seen or not all("integer cents" in prompt for prompt in seen):
        failures.append("the learned rule was not in the system prompt")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
