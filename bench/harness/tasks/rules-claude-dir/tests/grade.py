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


SCRIPT = pathlib.Path("/app/scripts/clean.sh")
lines = SCRIPT.read_text().splitlines() if SCRIPT.is_file() else []
if len(lines) < 2 or not lines[0].startswith("#!"):
    failures.append("clean.sh has no shebang line")
if len(lines) < 2 or lines[1] != "# owner: platform-team":
    failures.append("the second line of clean.sh is not the owner comment")
if SCRIPT.is_file():
    work = pathlib.Path(tempfile.mkdtemp())
    (work / "sub").mkdir()
    (work / "a.tmp").write_text("a")
    (work / "keep.txt").write_text("k")
    (work / "sub" / "b.tmp").write_text("b")
    (work / "sub" / "keep.md").write_text("k")
    run = subprocess.run(["bash", str(SCRIPT), str(work)], capture_output=True)
    if run.returncode != 0:
        failures.append("clean.sh failed on a plain directory")
    left = sorted(str(p.relative_to(work)) for p in work.rglob("*") if p.is_file())
    if left != ["keep.txt", "sub/keep.md"]:
        failures.append(f"clean.sh left the wrong files: {left}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    seen = prompts(trial)
    if not seen or not all("# owner: platform-team" in prompt for prompt in seen):
        failures.append("the .claude/rules rule was not in the system prompt")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
