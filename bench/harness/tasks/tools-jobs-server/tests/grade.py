import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

import os
import re

DROPPED = re.compile(r'"?(?:stdout|stderr)DroppedLines"?\s*[:=]\s*([1-9][0-9]*)')

def server_processes():
    found = []
    for entry in os.listdir("/proc"):
        if not entry.isdigit():
            continue
        try:
            raw = pathlib.Path("/proc", entry, "cmdline").read_bytes()
        except OSError:
            continue
        text = raw.replace(b"\0", b" ").decode("utf-8", "replace")
        if "http.server" in text and "8931" in text:
            found.append(text.strip())
    return found

failures = []
status = pathlib.Path("/app/out/status.txt")
if not status.is_file() or status.read_text().strip() != "service-status: green-3141":
    failures.append("/app/out/status.txt does not hold the status body")
noise = pathlib.Path("/app/out/noise.txt")
if not noise.is_file() or noise.read_text().strip() != "DONE code=7391":
    failures.append("/app/out/noise.txt does not hold the last noisy line")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    servers = trial.query("SELECT id, state FROM jobs WHERE command LIKE '%http.server%'")
    if not servers:
        failures.append("the server never ran as a job")
    for server in servers:
        signals = trial.query(
            "SELECT accepted FROM job_signal_requests WHERE job_id = ?",
            (server["id"],),
        )
        if not any(row["accepted"] for row in signals):
            failures.append("the server job was never stopped with signalJob")
        if server["state"] == "running":
            failures.append("the server job is still running")
    if not trial.calls_named("signalJob", include_children=False):
        failures.append("signalJob was never called")
    results = trial.tool_results(include_children=False)
    if not any(DROPPED.search(result["content"]) for result in results.values()):
        failures.append("no job result reported a non-zero dropped line count")
    left = server_processes()
    if left:
        failures.append(f"a server process is still running: {left}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
