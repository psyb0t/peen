"""Shared fixtures for the harness tool tests.

The tools under test import each other from ``bench/harness`` and the
library from ``bench/harness/lib``, and the proxy tests import
``bench/proxy.py``, so all three go on the import path first.
"""

import pathlib
import sys

import pytest

TESTS_DIR = pathlib.Path(__file__).resolve().parent
HARNESS_DIR = TESTS_DIR.parent
BENCH_DIR = HARNESS_DIR.parent
for path in (TESTS_DIR, HARNESS_DIR / "lib", HARNESS_DIR, BENCH_DIR):
    sys.path.insert(0, str(path))

import trialbuilder  # noqa: E402

TRIAL_NAME = "tools-sample__abc123"


@pytest.fixture
def build_trial(tmp_path):
    """Return a factory for trial directories under a fresh temporary root."""

    def build(name=TRIAL_NAME, reward=1.0, root=None):
        return trialbuilder.TrialBuilder(root or tmp_path, name, reward)

    return build


@pytest.fixture
def clean_trial(build_trial):
    """A trial with one completed turn: two model calls and one tool call."""
    builder = build_trial().add_turn()
    builder.add_model_call(["readFile"])
    builder.add_model_call()
    builder.add_frames(
        trialbuilder.frame(1, "turn.started", {}),
        trialbuilder.tool_use(2, "a", "readFile", {"path": "/app/x"}),
        trialbuilder.tool_result(3, "a", "readFile"),
        trialbuilder.frame(4, "turn.completed", {"text": "done", "finishReason": "stop"}),
    )
    return builder
