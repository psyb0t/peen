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


NOTES = pathlib.Path("/app/notes/today.md")
lines = [line.strip() for line in NOTES.read_text().splitlines() if line.strip()] if NOTES.is_file() else []
if lines != ["STATUS: REVIEWED", "Remember the milk."]:
    failures.append(f"today.md holds {lines}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    seen = prompts(trial)
    if not seen:
        failures.append("no turn recorded a system prompt")
    for prompt in seen:
        draft = prompt.find("STATUS: DRAFT")
        reviewed = prompt.find("STATUS: REVIEWED")
        if draft < 0 or reviewed < 0:
            failures.append("a layer's notes rule is missing from the system prompt")
        elif draft > reviewed:
            failures.append("the config layer's rule comes after the workspace layer's")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
