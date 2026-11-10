import hashlib
import pathlib

root = pathlib.Path("/app/data")
root.mkdir(parents=True, exist_ok=True)
with open(root / "events.log", "w") as handle:
    for number in range(1, 20001):
        token = hashlib.md5(str(number).encode()).hexdigest()[:8]
        handle.write(f"seq={number} node=n{number % 7} token={token}\n")
