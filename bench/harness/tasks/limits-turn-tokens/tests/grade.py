import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
start = pathlib.Path("/app/chain/start.txt")
if not start.is_file() or start.read_text() != "next: amber.txt\n":
    failures.append("the chain was changed")

LIMIT = 20000
if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    states = [row["state"] for row in trial.query("SELECT state FROM turns")]
    if states != ["failed"]:
        failures.append(f"turn states were {states}, expected one failed turn")
    ends = trial.of_type(peenaudit.TURN_FAILED)
    text = " ".join(str(v) for v in (ends[0].get("data") or {}).values()).lower() if ends else ""
    if "token limit" not in text:
        failures.append("the failed turn does not name the token limit")
    rounds = trial.rounds()
    used = 0
    crossed = None
    for index, row in enumerate(rounds):
        used += row["total_tokens"]
        if used >= LIMIT:
            crossed = index
            break
    if crossed is None:
        failures.append(f"the rounds billed {used} tokens and never crossed {LIMIT}")
    elif len(rounds) > crossed + 1:
        failures.append(
            f"{len(rounds) - crossed - 1} model calls began after round {crossed + 1} crossed the limit"
        )
    print("INFO: rounds", len(rounds), "tokens", used)

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
