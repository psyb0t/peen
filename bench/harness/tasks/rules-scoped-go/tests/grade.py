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


def go_test(package_dir, test_source):
    """Run a hidden Go test against a copy of the workspace."""
    work = pathlib.Path(tempfile.mkdtemp()) / "app"
    shutil.copytree("/app", work, ignore=shutil.ignore_patterns(".git", ".plans"))
    (work / package_dir / "hidden_test.go").write_text(test_source)
    env = dict(os.environ, GOCACHE="/tmp/grade-gocache", HOME="/tmp", GOPROXY="off")
    run = subprocess.run(
        ["/usr/local/go/bin/go", "test", "./..."],
        cwd=work,
        env=env,
        capture_output=True,
        text=True,
    )
    return run.returncode, run.stdout + run.stderr


SOURCE = pathlib.Path("/app/textutil/textutil.go")
HIDDEN_TEST = """package textutil

import "testing"

func TestReverseHidden(t *testing.T) {
	if got := Reverse("h\u00e9llo"); got != "oll\u00e9h" {
		t.Fatalf("Reverse = %q", got)
	}
	if got := Reverse(""); got != "" {
		t.Fatalf("Reverse of empty = %q", got)
	}
}
"""

text = SOURCE.read_text() if SOURCE.is_file() else ""
if not re.search(r"^// Stability: experimental\s*\nfunc Reverse\(", text, re.M):
    failures.append("Reverse does not end its doc comment with the stability line")
code, output = go_test("textutil", HIDDEN_TEST)
if code != 0:
    failures.append("Reverse does not behave: " + output.strip()[-300:])

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    blocks = rule_blocks(trial)
    if not any(block.startswith('<rule name="go"') for block in blocks):
        failures.append("the Go rule never reached the model after a tool result")
    if any("Stability: experimental" in prompt for prompt in prompts(trial)):
        failures.append("the scoped rule was in the system prompt")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
