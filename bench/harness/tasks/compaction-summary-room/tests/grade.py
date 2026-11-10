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
wrong = [key for key, code in FACTS.items() if answers.get(key) != code]
if answers and wrong:
    failures.append(f"codes missing or wrong: {wrong}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    states = [
        row["state"]
        for row in trial.query("SELECT state FROM turns ORDER BY started_at")
    ]
    if states != ["completed"] * 7:
        failures.append(f"turn states were {states}")
    compactions = trial.query("SELECT summary_token_count FROM compactions")
    if not compactions:
        failures.append("no compaction was stored, so the smaller room never worked")
    if any(row["summary_token_count"] <= 0 for row in compactions):
        failures.append("a stored compaction holds an empty summary")
    print("INFO: compactions", len(compactions))

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
