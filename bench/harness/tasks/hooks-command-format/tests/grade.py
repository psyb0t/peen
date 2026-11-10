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


HELLO = pathlib.Path("/app/hello/hello.go")
HIDDEN_TEST = """package hello

import "testing"

func TestHelloHidden(t *testing.T) {
	if got := Hello(); got != "hello" {
		t.Fatalf("Hello = %q", got)
	}
}
"""
text = HELLO.read_text() if HELLO.is_file() else ""
if not text.startswith("// Copyright Example Corp\n"):
    failures.append("hello.go does not start with the license header")
if text.count("// Copyright Example Corp") != 1:
    failures.append("hello.go holds the license header more than once")
code, output = go_test("hello", HIDDEN_TEST)
if code != 0:
    failures.append("Hello does not behave: " + output.strip()[-300:])

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    log = pathlib.Path("/tmp/stamp-hook.log")
    runs = []
    if log.is_file():
        runs = [json.loads(line) for line in log.read_text().splitlines() if line.strip()]
    if not any("/app/hello/hello.go" in run.get("paths", []) for run in runs):
        failures.append("the format hook never ran on hello.go")
    written = [
        call
        for call in trial.tool_calls(include_children=False)
        if call["name"] in (peenaudit.TOOL_WRITE_FILE, peenaudit.TOOL_EDIT_FILE)
        and "/app/hello/hello.go" in peenaudit.call_paths(call)
    ]
    if not written:
        failures.append("hello.go was not written with a file tool")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
