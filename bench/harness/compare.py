"""Compare two finished harness jobs task by task.

Run the same tasks twice with one setting changed, such as two harness builds
on the same model, or the default harness and the blank-agent control, then
compare. For every task in both jobs it reports the two pass rates, their
difference, and whether their 95 percent intervals overlap. Intervals that do
not overlap mark a change the attempts can tell apart from noise. It also
lists every measurement whose median moved by more than a quarter.

It writes ``compare/<first>__<second>.md`` and ``.json`` under the bench
state directory and prints the Markdown path.
"""

import argparse
import json
import logging
import logging.handlers
import pathlib
import sys

# The tools run with python -I, which leaves the script's own directory off
# sys.path, so the sibling report module needs it added.
sys.path.insert(0, str(pathlib.Path(__file__).parent))
sys.path.insert(0, str(pathlib.Path(__file__).parent / "lib"))

import benchstats  # noqa: E402
import report  # noqa: E402

COMPARE_DIR = "compare"
LOG_NAME = "compare.log"
LOG_MAX_BYTES = 10_000_000
LOG_BACKUPS = 3
# A median that moved by more than this share of its old value is listed.
METRIC_CHANGE_THRESHOLD = 0.25
PERCENT = 100

logger = logging.getLogger(__name__)


def configure_logging(state_dir):
    formatter = report.JSONFormatter()
    handlers = [
        logging.StreamHandler(sys.stderr),
        logging.handlers.RotatingFileHandler(
            state_dir / LOG_NAME, maxBytes=LOG_MAX_BYTES, backupCount=LOG_BACKUPS
        ),
    ]
    for handler in handlers:
        handler.setFormatter(formatter)
    logging.basicConfig(level=logging.INFO, handlers=handlers)


def compare_summaries(first, second):
    return {
        "first": {"rate": first["rate"], "interval": first["interval"]},
        "second": {"rate": second["rate"], "interval": second["interval"]},
        "difference": second["rate"] - first["rate"],
        "distinct": not benchstats.intervals_overlap(
            first["interval"], second["interval"]
        ),
    }


def metric_changes(first, second):
    changes = {}
    for name, values in first["metrics"].items():
        before = values["median"]
        after = (second["metrics"].get(name) or {}).get("median")
        if before is None or after is None:
            continue
        if before == 0:
            if after != 0:
                changes[name] = {"first": before, "second": after, "change": None}
            continue
        change = (after - before) / before
        if abs(change) > METRIC_CHANGE_THRESHOLD:
            changes[name] = {"first": before, "second": after, "change": change}
    return changes


def build_comparison(first_job, second_job, first, second):
    shared = sorted(set(first["tasks"]) & set(second["tasks"]))
    return {
        "first": first_job,
        "second": second_job,
        "firstSettings": first["settings"],
        "secondSettings": second["settings"],
        "suite": compare_summaries(first["suite"], second["suite"]),
        "groups": {
            name: compare_summaries(first["groups"][name], second["groups"][name])
            for name in sorted(set(first["groups"]) & set(second["groups"]))
        },
        "tasks": {
            name: compare_summaries(first["tasks"][name], second["tasks"][name])
            for name in shared
        },
        "onlyFirst": sorted(set(first["tasks"]) - set(second["tasks"])),
        "onlySecond": sorted(set(second["tasks"]) - set(first["tasks"])),
        "metrics": metric_changes(first["suite"], second["suite"]),
    }


def signed_percent(value):
    return f"{value * PERCENT:+.0f} points"


def comparison_row(name, item):
    marker = "yes" if item["distinct"] else ""
    return (
        f"| {name} | {report.percent(item['first']['rate'])} | "
        f"{report.percent(item['second']['rate'])} | "
        f"{signed_percent(item['difference'])} | {marker} |"
    )


def render_markdown(comparison):
    lines = [
        f"# Harness comparison: {comparison['first']} and {comparison['second']}",
        "",
        "Rates are trials that passed both the grader and every hard invariant. "
        "Outside noise means the two 95% intervals do not overlap.",
        "",
        "| Name | First | Second | Change | Outside noise |",
        "| --- | --- | --- | --- | --- |",
        comparison_row(report.SUITE_ROW, comparison["suite"]),
    ]
    lines += [comparison_row(name, item) for name, item in comparison["groups"].items()]
    lines += ["", "## Tasks", "", "| Task | First | Second | Change | Outside noise |"]
    lines += ["| --- | --- | --- | --- | --- |"]
    lines += [comparison_row(name, item) for name, item in comparison["tasks"].items()]
    lines += ["", "## Measurements that moved", ""]
    if not comparison["metrics"]:
        lines.append("No median moved by more than a quarter.")
    for name, item in comparison["metrics"].items():
        change = "from zero" if item["change"] is None else f"{item['change']:+.0%}"
        lines.append(
            f"- `{name}`: {report.metric_value(item['first'])} to "
            f"{report.metric_value(item['second'])} ({change})"
        )
    for label, key in (("Only in the first job", "onlyFirst"), ("Only in the second job", "onlySecond")):
        if comparison[key]:
            lines += ["", f"{label}: {', '.join(comparison[key])}"]
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--state-dir", required=True, type=pathlib.Path)
    parser.add_argument("first")
    parser.add_argument("second")
    args = parser.parse_args()
    configure_logging(args.state_dir)

    reports = []
    for job in (args.first, args.second):
        job_dir = args.state_dir / report.JOBS_DIR / job
        if not job_dir.is_dir():
            logger.error("job not found", extra={"path": str(job_dir)})
            return 1
        reports.append(report.build_report(job, job_dir))
    comparison = build_comparison(args.first, args.second, *reports)
    output = args.state_dir / COMPARE_DIR
    output.mkdir(parents=True, exist_ok=True)
    stem = f"{args.first}__{args.second}"
    (output / f"{stem}.json").write_text(json.dumps(comparison, indent=2) + "\n")
    (output / f"{stem}.md").write_text(render_markdown(comparison))
    logger.info("comparison written", extra={"path": str(output / f"{stem}.md")})
    print(output / f"{stem}.md")
    return 0


if __name__ == "__main__":
    sys.exit(main())
