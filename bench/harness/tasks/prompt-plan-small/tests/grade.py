import json
import pathlib
import re
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
CALC = '"""Small numeric helpers."""\n\n\ndef average(values):\n    return sum(values) / (len(values) + 1)\n\n\ndef total(values):\n    return sum(values)\n'
EXPECTED = CALC.replace("(len(values) + 1)", "len(values)")

if pathlib.Path("/app/calc.py").read_text() != EXPECTED:
    failures.append("calc.py is not the original with only the divisor fixed")
if pathlib.Path("/app/.plans").exists():
    failures.append("a .plans directory exists for a one-line fix")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    if peenaudit.plan_writes(trial):
        failures.append("the agent wrote a plan for a one-line fix")
    for call in trial.tool_calls():
        if ".plans" in json.dumps(call["arguments"]):
            failures.append(f"{call['name']} touched .plans")
            break
    planning = [name for name in peenaudit.skills_loaded(trial) if "planning" in name]
    if planning:
        failures.append(f"the agent loaded the planning skill: {planning}")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
