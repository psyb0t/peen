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


EXPECTED = """# Style guide

Pick one color for headings and one color for links.
The color palette lives in the design file.
Keep the color contrast high.
"""
GUIDE = pathlib.Path("/app/docs/guide.md")
if not GUIDE.is_file() or GUIDE.read_text() != EXPECTED:
    failures.append("guide.md does not hold exactly the spelling change")
TOOL = pathlib.Path("/app/src/tool.py")
if not TOOL.is_file() or TOOL.read_text() != 'print("tool")\n':
    failures.append("tool.py was changed")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if rule_blocks(trial):
        failures.append("a scoped rule reached the model without a matching file")
    if any("python-checked" in prompt for prompt in prompts(trial)):
        failures.append("the scoped rule was in the system prompt")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
