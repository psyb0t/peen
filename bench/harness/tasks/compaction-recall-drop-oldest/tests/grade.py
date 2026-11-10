import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

failures = []
DROP_RECORD = "dropping oldest transcript unit"
MAX_CONTEXT_TOKENS = 24576


def parse_answers(path):
    answers = {}
    for line in pathlib.Path(path).read_text().splitlines():
        key, _, value = line.partition("=")
        answers[key.strip()] = value.strip()
    return answers

FACTS = {"K01": "amber-311", "K02": "birch-448", "K03": "cedar-585", "K04": "delta-722", "K05": "ember-859", "K06": "fjord-996", "K07": "garnet-233", "K08": "harbor-370", "K09": "indigo-507", "K10": "juniper-644", "K11": "kestrel-781", "K12": "lagoon-918", "K13": "marble-155", "K14": "nectar-292", "K15": "onyx-429", "K16": "pewter-566", "K17": "quartz-703", "K18": "russet-840", "K19": "saffron-977", "K20": "tundra-214", "K21": "umber-351", "K22": "violet-488", "K23": "willow-625", "K24": "xenon-762", "K25": "yarrow-899", "K26": "zephyr-136", "K27": "basalt-273", "K28": "cobalt-410", "K29": "dune-547", "K30": "falcon-684"}
LATE_KEYS = ["K25", "K26", "K27", "K28", "K29", "K30"]
answers = {}
answers_path = pathlib.Path("/app/answers.txt")
if answers_path.is_file():
    answers = parse_answers(answers_path)
else:
    failures.append("/app/answers.txt is missing")
if answers:
    absent = [key for key in FACTS if key not in answers]
    if absent:
        failures.append(f"no line for: {absent}")
    invented = [
        key for key, code in FACTS.items()
        if key in answers and answers[key] not in (code, "unknown")
    ]
    if invented:
        failures.append(f"wrong codes given instead of unknown: {invented}")
    late_wrong = [key for key in LATE_KEYS if answers.get(key) != FACTS[key]]
    if late_wrong:
        failures.append(f"recent codes lost: {late_wrong}")
    print("INFO: recalled", sum(answers.get(k) == v for k, v in FACTS.items()))

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    states = [
        row["state"]
        for row in trial.query("SELECT state FROM turns ORDER BY started_at")
    ]
    if states != ["completed"] * 9:
        failures.append(f"turn states were {states}")
    sent = len(trial.of_type(peenaudit.USER_MESSAGE_CREATED))
    if sent != 9:
        failures.append(f"{sent} messages were accepted, expected 9")

    compactions = trial.query("SELECT id FROM compactions")
    if compactions:
        failures.append("drop-oldest stored a compaction, which it must not")
    runtime_log = pathlib.Path("/peen/runtime.log")
    log_text = runtime_log.read_text(errors="replace") if runtime_log.is_file() else ""
    if DROP_RECORD not in log_text:
        failures.append("the runtime log never recorded dropping a transcript unit")
    prompts = [row["prompt_tokens"] or 0 for row in trial.rounds()]
    if prompts and max(prompts) > MAX_CONTEXT_TOKENS:
        failures.append(f"a request of {max(prompts)} tokens went over the context limit")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
