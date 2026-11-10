import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
import json
import sqlite3

path = pathlib.Path("/app/metrics.txt")
reported = {}
if path.is_file():
    for line in path.read_text().splitlines():
        name, _, value = line.partition("=")
        if value.strip().isdigit():
            reported[name.strip()] = int(value)
if sorted(reported) != ["readFile", "writeFile"]:
    failures.append("/app/metrics.txt does not hold readFile and writeFile counts")


def summary_rows(content):
    """The rows of a readMetrics result, which may sit inside a wrapper."""
    start, end = content.find("{"), content.rfind("}")
    if start < 0 or end < start:
        return None
    try:
        return json.loads(content[start : end + 1]).get("rows")
    except ValueError:
        return None


def tally(names):
    counts = {}
    for name in names:
        counts[name] = counts.get(name, 0) + 1
    return counts


if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    calls = trial.tool_calls(include_children=False)
    asked = [
        call for call in calls
        if call["name"] == "readMetrics" and call["arguments"].get("summary") == "tools"
    ]
    if not asked:
        failures.append("the agent never read the tools summary")
    else:
        call = asked[-1]
        before = tally(c["name"] for c in calls[: calls.index(call)])
        result = trial.tool_results(include_children=False).get(call["callId"])
        rows = summary_rows(result["content"]) if result else None
        if rows is None:
            failures.append("the tools summary result could not be read")
        else:
            counts = {row["name"]: row["count"] for row in rows}
            if counts != before:
                failures.append(f"the summary said {counts}, the frames hold {before}")
            for name in ("readFile", "writeFile"):
                if reported.get(name) != counts.get(name):
                    failures.append(
                        f"{name} was reported as {reported.get(name)}, the summary gave {counts.get(name)}"
                    )
        rounds = trial.rounds()
        index = next(
            (
                i for i, row in enumerate(rounds)
                if any(c["name"] == "readMetrics" for c in row["toolCalls"])
            ),
            None,
        )
        if index is None:
            failures.append("no model call asked for readMetrics")
        else:
            recorded = tally(c["name"] for row in rounds[:index] for c in row["toolCalls"])
            if recorded != before:
                failures.append(f"model_calls hold {recorded}, the frames hold {before}")
    database = trial.dir / "state" / "metrics.db"
    if not database.is_file():
        failures.append("state/metrics.db was not left behind")
    else:
        connection = sqlite3.connect(database.resolve().as_uri() + "?mode=ro", uri=True)
        try:
            stored = dict(
                connection.execute(
                    "SELECT name, count(*) FROM metric_observations "
                    "WHERE kind = 'tool' GROUP BY name"
                ).fetchall()
            )
        finally:
            connection.close()
        for name, count in tally(c["name"] for c in calls).items():
            if name != "readMetrics" and stored.get(name) != count:
                failures.append(
                    f"metrics.db holds {stored.get(name)} {name} rows, the frames hold {count}"
                )

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
