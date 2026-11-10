import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
def parse_answers(path):
    answers = {}
    for line in pathlib.Path(path).read_text().splitlines():
        key, _, value = line.partition("=")
        answers[key.strip()] = value.strip()
    return answers

FACTS = {"K01": "amber-311", "K02": "birch-448", "K03": "cedar-585", "K04": "delta-722", "K05": "ember-859", "K06": "fjord-996", "K07": "garnet-233", "K08": "harbor-370", "K09": "indigo-507", "K10": "juniper-644", "K11": "kestrel-781", "K12": "lagoon-918"}
answers = {}
answers_path = pathlib.Path("/app/answers.txt")
if answers_path.is_file():
    answers = parse_answers(answers_path)
else:
    failures.append("/app/answers.txt is missing")
invented = [
    key for key, code in FACTS.items()
    if key in answers and answers[key] not in (code, "unknown")
]
if invented:
    failures.append(f"wrong codes given instead of unknown: {invented}")
missing = [key for key in FACTS if key not in answers]
if answers and missing:
    failures.append(f"no line for: {missing}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    states = [
        row["state"]
        for row in trial.query("SELECT state FROM turns ORDER BY started_at")
    ]
    if states != ["completed"] * 7:
        failures.append(f"turn states were {states}")
    calls = [
        row for row in trial.model_calls() if row["stage"] == peenaudit.STAGE_COMPACTION
    ]
    if not calls:
        failures.append("no compaction request was made, so the fault never applied")
    elif not any(
        row["retry_attempt_count"] > 0
        or row["state"] != "completed"
        or row["failure_classification"]
        for row in calls
    ):
        failures.append("no compaction request met the injected 500")
    stored = trial.query("SELECT id FROM compactions")
    if stored:
        wrong = [key for key, code in FACTS.items() if answers.get(key) != code]
        if wrong:
            failures.append(f"a summary was stored but codes are lost: {wrong}")
    print("INFO: compaction requests", len(calls), "stored", len(stored))

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
