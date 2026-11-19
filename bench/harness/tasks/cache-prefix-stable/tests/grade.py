import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

WORDS = {"1.txt": "amber", "2.txt": "cobalt", "3.txt": "saffron"}
TURNS = len(WORDS)

failures = []
for name, word in WORDS.items():
    path = pathlib.Path("/app/out") / name
    if not path.is_file() or path.read_text().strip() != word:
        failures.append(f"/app/out/{name} does not hold {word}")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    turns = trial.query(
        "SELECT id, prompt_snapshot_hash FROM turns ORDER BY started_at"
    )
    if len(turns) != TURNS:
        failures.append(f"{len(turns)} turns ran, {TURNS} expected")
    prompts = {turn["prompt_snapshot_hash"] for turn in turns}
    if len(prompts) != 1:
        failures.append(f"the turns used {len(prompts)} system prompts, 1 expected")

    # The first call of each turn after the first resends the conversation
    # so far. A prompt that stayed the same lets the provider read it back
    # from its cache.
    for turn in turns[1:]:
        first = trial.query(
            """
            SELECT mc.cache_read_tokens
            FROM model_calls mc
            JOIN model_runs mr ON mr.id = mc.model_run_id
            WHERE mr.turn_id = ? AND mr.stage = 'turn'
            ORDER BY mc.started_at
            LIMIT 1
            """,
            (turn["id"],),
        )
        if not first or not first[0]["cache_read_tokens"]:
            failures.append(f"turn {turn['id']} read nothing from the cache")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
