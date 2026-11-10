"""Write the report of a finished, audited harness job.

It reads every trial's ``audit.json`` and writes ``report.json`` and
``report.md`` into the job directory: the pass rate of each task and each
group with a 95 percent Wilson interval, which hard invariants broke and how
often, and the median and 95th percentile of every measurement. A task's
group is its name up to the first hyphen, such as ``tools`` for
``tools-readfile-window``.
"""

import argparse
import datetime
import json
import logging
import logging.handlers
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).parent / "lib"))

import benchstats  # noqa: E402

JOBS_DIR = "jobs"
AUDIT_NAME = "audit.json"
JOB_CONFIG = "config.json"
REPORT_JSON = "report.json"
REPORT_MARKDOWN = "report.md"
LOG_NAME = "report.log"
LOG_MAX_BYTES = 10_000_000
LOG_BACKUPS = 3
GROUP_SEPARATOR = "-"
PERCENT = 100
SUITE_ROW = "all"

logger = logging.getLogger(__name__)


class JSONFormatter(logging.Formatter):
    FIELDS = ("job", "path", "trials", "tasks")

    def format(self, record):
        entry = {
            "time": datetime.datetime.fromtimestamp(
                record.created, datetime.UTC
            ).isoformat(),
            "level": record.levelname.lower(),
            "file": record.filename,
            "line": record.lineno,
            "func": record.funcName,
            "msg": record.getMessage(),
        }
        for field in self.FIELDS:
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


def load_audits(job_dir):
    audits = []
    for path in sorted(job_dir.glob(f"*/{AUDIT_NAME}")):
        try:
            audits.append(json.loads(path.read_text()))
        except (OSError, ValueError) as err:
            raise RuntimeError(f"read {path}") from err
    return audits


def group_of(task):
    return task.split(GROUP_SEPARATOR, 1)[0]


def summarize(audits):
    """Pass rate, broken invariants and measurements for a set of trials."""
    passes = sum(1 for audit in audits if audit["passed"])
    low, high = benchstats.wilson_interval(passes, len(audits))
    broken = {}
    for audit in audits:
        for name, item in audit["hard"].items():
            if not item["ok"]:
                broken[name] = broken.get(name, 0) + 1
    metric_names = sorted({name for audit in audits for name in audit["metrics"]})
    metrics = {}
    for name in metric_names:
        values = [audit["metrics"].get(name) for audit in audits]
        metrics[name] = {
            "median": benchstats.median(values),
            "p95": benchstats.p95(values),
        }
    return {
        "trials": len(audits),
        "passes": passes,
        "rewardPasses": sum(1 for audit in audits if audit["reward"] >= 1.0),
        "rate": passes / len(audits) if audits else 0.0,
        "interval": [low, high],
        "brokenInvariants": broken,
        "metrics": metrics,
    }


def job_settings(job_dir):
    try:
        config = json.loads((job_dir / JOB_CONFIG).read_text())
    except (OSError, ValueError):
        return {}
    agent = (config.get("agents") or [{}])[0]
    return {
        "model": agent.get("model_name"),
        "kwargs": agent.get("kwargs") or {},
        "attempts": config.get("n_attempts"),
    }


def build_report(job, job_dir):
    audits = load_audits(job_dir)
    by_task, by_group = {}, {}
    for audit in audits:
        by_task.setdefault(audit["task"], []).append(audit)
        by_group.setdefault(group_of(audit["task"]), []).append(audit)
    return {
        "job": job,
        "settings": job_settings(job_dir),
        "suite": summarize(audits),
        "groups": {name: summarize(items) for name, items in sorted(by_group.items())},
        "tasks": {name: summarize(items) for name, items in sorted(by_task.items())},
    }


def percent(value):
    return f"{value * PERCENT:.0f}%"


def rate_row(name, summary):
    low, high = summary["interval"]
    broken = ", ".join(
        f"{key} {count}" for key, count in sorted(summary["brokenInvariants"].items())
    )
    return (
        f"| {name} | {summary['passes']} of {summary['trials']} | "
        f"{percent(summary['rate'])} | {percent(low)} to {percent(high)} | "
        f"{summary['rewardPasses']} | {broken or 'none'} |"
    )


RATE_HEADER = (
    "| Name | Passed | Rate | 95% interval | Grader passed | Broken invariants |",
    "| --- | --- | --- | --- | --- | --- |",
)


def metric_value(value):
    if value is None:
        return ""
    if isinstance(value, float) and not value.is_integer():
        return f"{value:.2f}"
    return f"{value:.0f}"


def render_markdown(report):
    settings = report["settings"]
    lines = [
        f"# Harness report: {report['job']}",
        "",
        f"Model `{settings.get('model')}`, {settings.get('attempts')} attempts per task.",
        "",
        "A trial passes when its grader gave reward 1 and every hard invariant held.",
        "",
        "## Groups",
        "",
        *RATE_HEADER,
        rate_row(SUITE_ROW, report["suite"]),
    ]
    lines += [rate_row(name, summary) for name, summary in report["groups"].items()]
    lines += ["", "## Tasks", "", *RATE_HEADER]
    lines += [rate_row(name, summary) for name, summary in report["tasks"].items()]
    lines += ["", "## Measurements", "", "| Measurement | Median | 95th percentile |"]
    lines += ["| --- | --- | --- |"]
    for name, values in report["suite"]["metrics"].items():
        lines.append(
            f"| {name} | {metric_value(values['median'])} | {metric_value(values['p95'])} |"
        )
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--state-dir", required=True, type=pathlib.Path)
    parser.add_argument("job")
    args = parser.parse_args()
    configure_logging(args.state_dir)

    job_dir = args.state_dir / JOBS_DIR / args.job
    if not job_dir.is_dir():
        logger.error("job not found", extra={"path": str(job_dir)})
        return 1
    report = build_report(args.job, job_dir)
    (job_dir / REPORT_JSON).write_text(json.dumps(report, indent=2) + "\n")
    (job_dir / REPORT_MARKDOWN).write_text(render_markdown(report))
    logger.info(
        "report written",
        extra={
            "job": args.job,
            "path": str(job_dir / REPORT_MARKDOWN),
            "trials": report["suite"]["trials"],
            "tasks": len(report["tasks"]),
        },
    )
    print(job_dir / REPORT_MARKDOWN)
    return 0


if __name__ == "__main__":
    sys.exit(main())
