"""Write one Harbor job configuration for a benchmark run.

``scripts/make/bench.sh`` runs this inside the driver image with the settings
below in the environment, then hands the written file to ``harbor run``.
Every path is the host path, because Harbor starts the task containers on the
host's Docker daemon and binds those paths into them.

The Aider Polyglot pool keeps only tasks whose grader works offline: C++, Go,
and Rust exercises without crate dependencies. Java, JavaScript and Python
graders fetch their test tools while grading, and the task container has no
network.

The eval suite is Peen's own: small tasks under ``bench/eval/tasks`` that each
check one behavior, such as ignoring an instruction planted in a file or
following a scoped rule. They ship with the source, so nothing is downloaded.
"""

import json
import logging
import logging.handlers
import os
import pathlib
import sys
import tomllib

ENV_SUITE = "BENCH_SUITE"
ENV_STATE_DIR = "BENCH_STATE_DIR"
ENV_SOURCE_DIR = "BENCH_SOURCE_DIR"
ENV_JOB = "BENCH_JOB"
ENV_TASKS = "BENCH_TASKS"
ENV_AGENT = "BENCH_AGENT"
ENV_MODEL = "BENCH_MODEL"
ENV_CONTEXT_TOKENS = "BENCH_CONTEXT_TOKENS"
ENV_COMPACTION_MODEL = "BENCH_COMPACTION_MODEL"
ENV_CONCURRENCY = "BENCH_CONCURRENCY"
SUITE_AIDER = "aider"
SUITE_LHTB = "lhtb"
SUITE_EVAL = "eval"
AGENT_PEEN = "peen"
AGENT_ORACLE = "oracle"
DATASET_DIRS = {SUITE_AIDER: "aider-polyglot", SUITE_LHTB: "lhtb"}
EVAL_TASKS_DIR = ("eval", "tasks")
SUITES = (SUITE_AIDER, SUITE_LHTB, SUITE_EVAL)
AIDER_PREFIX = "polyglot_"
AIDER_OFFLINE_LANGUAGES = ("cpp", "go", "rust")
AIDER_RUST = "rust"
CARGO_MANIFEST = "Cargo.toml"
CARGO_DEPENDENCY_TABLES = ("dependencies", "dev-dependencies")
LHTB_TASKS = (
    "langchain-version-migration",
    "rush_hour_campaign",
    "snake_maze_campaign",
    "sudoku-recovery",
)
# Aider tasks run under the agent timeout Harbor applies to every trial. LHTB
# tasks set their own longer timeouts, so the adapter's run timeout follows
# them instead.
AIDER_AGENT_TIMEOUT_SECONDS = 1860
LHTB_RUN_TIMEOUT_SECONDS = 5280
LHTB_MAX_TOOL_ROUNDS = 2000
DEFAULT_CONCURRENCY = {SUITE_AIDER: 3, SUITE_LHTB: 2, SUITE_EVAL: 3}
DEFAULT_MODEL = "zai/glm-5.3"
AGENT_IMPORT_PATH = "peen_agent:PeenAgent"
SANDBOX_FILE = "sandbox.yaml"
PROVIDER_DIR = "provider"
PROVIDER_TARGET = "/provider"
JOBS_DIR = "jobs"
DATASETS_DIR = "datasets"
CONFIG_DIR = "job-configs"
LOG_NAME = "job.log"
LOG_MAX_BYTES = 10_000_000
LOG_BACKUPS = 3

logger = logging.getLogger(__name__)


class JobError(Exception):
    """The job settings are invalid."""


class JSONFormatter(logging.Formatter):
    def format(self, record):
        entry = {
            "level": record.levelname.lower(),
            "file": record.filename,
            "line": record.lineno,
            "func": record.funcName,
            "msg": record.getMessage(),
        }
        for field in ("job", "suite", "agent", "tasks", "path", "task", "reason"):
            if hasattr(record, field):
                entry[field] = getattr(record, field)
        return json.dumps(entry, sort_keys=True)


def configure_logging(state_dir):
    formatter = JSONFormatter()
    handlers = [
        logging.StreamHandler(sys.stderr),
        logging.handlers.RotatingFileHandler(
            state_dir / LOG_NAME, maxBytes=LOG_MAX_BYTES, backupCount=LOG_BACKUPS
        ),
    ]
    for handler in handlers:
        handler.setFormatter(formatter)
    logging.basicConfig(level=logging.INFO, handlers=handlers)


def required(name):
    value = os.environ.get(name, "")
    if not value:
        raise JobError(f"{name} is not set")
    return value


def has_crate_dependencies(task_dir):
    for manifest in task_dir.rglob(CARGO_MANIFEST):
        try:
            data = tomllib.loads(manifest.read_text())
        except (OSError, tomllib.TOMLDecodeError) as err:
            raise JobError(f"read {manifest}") from err
        if any(data.get(table) for table in CARGO_DEPENDENCY_TABLES):
            return True
    return False


def offline_aider_tasks(dataset):
    tasks = []
    for task_dir in sorted(dataset.iterdir()):
        language = task_dir.name.removeprefix(AIDER_PREFIX).split("_", 1)[0]
        if language not in AIDER_OFFLINE_LANGUAGES:
            continue
        if language == AIDER_RUST and has_crate_dependencies(task_dir):
            logger.debug(
                "task skipped",
                extra={"task": task_dir.name, "reason": "crate_dependencies"},
            )
            continue
        tasks.append(task_dir.name)
    return tasks


def task_names(suite, dataset):
    chosen = os.environ.get(ENV_TASKS, "").split()
    if chosen:
        return chosen
    if suite == SUITE_AIDER:
        return offline_aider_tasks(dataset)
    if suite == SUITE_EVAL:
        return sorted(path.name for path in dataset.iterdir() if path.is_dir())
    return list(LHTB_TASKS)


def dataset_dir(suite, state_dir, source_dir):
    if suite == SUITE_EVAL:
        return source_dir.joinpath(*EVAL_TASKS_DIR)
    return state_dir / DATASETS_DIR / DATASET_DIRS[suite]


def agent_config(suite, agent):
    if agent == AGENT_ORACLE:
        return {"name": AGENT_ORACLE}
    kwargs = {}
    context_tokens = os.environ.get(ENV_CONTEXT_TOKENS, "")
    if context_tokens:
        kwargs["context_window_tokens"] = context_tokens
    compaction_model = os.environ.get(ENV_COMPACTION_MODEL, "")
    if compaction_model:
        kwargs["compaction_model"] = compaction_model
    config = {
        "import_path": AGENT_IMPORT_PATH,
        "model_name": os.environ.get(ENV_MODEL) or DEFAULT_MODEL,
    }
    if suite == SUITE_AIDER:
        config["override_timeout_sec"] = AIDER_AGENT_TIMEOUT_SECONDS
    elif suite == SUITE_LHTB:
        kwargs["run_timeout_sec"] = LHTB_RUN_TIMEOUT_SECONDS
        kwargs["max_tool_rounds"] = LHTB_MAX_TOOL_ROUNDS
    if kwargs:
        config["kwargs"] = kwargs
    return config


def main():
    suite = required(ENV_SUITE)
    if suite not in SUITES:
        raise JobError(f"{ENV_SUITE} must be one of {sorted(SUITES)}")
    agent = os.environ.get(ENV_AGENT) or AGENT_PEEN
    if agent not in (AGENT_PEEN, AGENT_ORACLE):
        raise JobError(f"{ENV_AGENT} must be {AGENT_PEEN} or {AGENT_ORACLE}")
    state_dir = pathlib.Path(required(ENV_STATE_DIR))
    source_dir = pathlib.Path(required(ENV_SOURCE_DIR))
    job = required(ENV_JOB)
    configure_logging(state_dir)

    dataset = dataset_dir(suite, state_dir, source_dir)
    if not dataset.is_dir():
        raise JobError(f"{dataset} is missing; run make bench-datasets")
    tasks = task_names(suite, dataset)
    concurrency = int(os.environ.get(ENV_CONCURRENCY) or DEFAULT_CONCURRENCY[suite])

    config = {
        "job_name": job,
        "jobs_dir": str(state_dir / JOBS_DIR),
        "n_attempts": 1,
        "n_concurrent_trials": concurrency,
        "quiet": True,
        "retry": {"max_retries": 0},
        "environment": {
            "type": "docker",
            "extra_docker_compose": [str(source_dir / SANDBOX_FILE)],
            "mounts": [
                {
                    "type": "bind",
                    "source": str(state_dir / PROVIDER_DIR),
                    "target": PROVIDER_TARGET,
                    "read_only": True,
                }
            ],
        },
        "agents": [agent_config(suite, agent)],
        "datasets": [{"path": str(dataset), "task_names": tasks}],
        "debug": True,
    }
    output = state_dir / CONFIG_DIR / f"{job}.json"
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(config, indent=2) + "\n")
    logger.info(
        "job configuration written",
        extra={
            "job": job,
            "suite": suite,
            "agent": agent,
            "tasks": len(tasks),
            "path": str(output),
        },
    )
    print(output)


if __name__ == "__main__":
    main()
