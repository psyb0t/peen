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
ORIGINALS = {'users.py': '"""User lookup helpers."""\n\nUSERS = {1: "ada", 2: "grace", 3: "linus"}\n\n\ndef fetch_user(user_id):\n    return USERS.get(user_id, "unknown")\n', 'api.py': 'from users import fetch_user\n\n\ndef handle(user_id):\n    return {"user": fetch_user(user_id)}\n', 'cli.py': 'from users import fetch_user\n\n\ndef main():\n    print(fetch_user(1))\n\n\nif __name__ == "__main__":\n    main()\n', 'jobs.py': 'import users\n\n\ndef nightly():\n    return [users.fetch_user(number) for number in (1, 2, 3)]\n'}

for name, original in ORIGINALS.items():
    path = pathlib.Path("/app", name)
    expected = original.replace("fetch_user", "load_user")
    if not path.is_file() or path.read_text() != expected:
        failures.append(f"{name} is not the original with only the rename applied")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    plan_calls = peenaudit.plan_writes(trial)
    plan_paths = {
        path
        for call in plan_calls
        for path in peenaudit.call_paths(call)
        if "/.plans/" in path
    }
    if len(plan_paths) != 1:
        failures.append(f"expected exactly one plan file, found {sorted(plan_paths)}")
    elif not re.fullmatch(r"/app/\.plans/[^/]+\.md", next(iter(plan_paths))):
        failures.append(f"the plan is not a .plans/*.md file: {sorted(plan_paths)}")

    root_calls = trial.tool_calls(include_children=False)
    source_edits = [
        call
        for call in root_calls
        if call["name"] in peenaudit.MUTATING_TOOLS
        and any(path.endswith(".py") for path in peenaudit.call_paths(call))
    ]
    if plan_calls and source_edits:
        if min(call["index"] for call in plan_calls) > source_edits[0]["index"]:
            failures.append("a source file was edited before the plan was written")
    skill_calls = [
        call
        for call in root_calls
        if call["name"] == peenaudit.TOOL_USE_SKILL
        and call["arguments"].get("name") in ("core:planning", "planning")
    ]
    if not skill_calls:
        failures.append("the agent never loaded core:planning")
    elif plan_calls and skill_calls[0]["index"] > min(call["index"] for call in plan_calls):
        failures.append("the plan was written before core:planning was loaded")

    ignore = pathlib.Path("/app/.gitignore").read_text().split()
    if not {".plans", ".plans/", "/.plans", "/.plans/"} & set(ignore):
        failures.append(".plans/ was not added to .gitignore")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
