import pathlib
import sys

sys.path.insert(0, "/peen/lib")
try:
    import peenaudit
except ImportError:
    peenaudit = None

TARGET = pathlib.Path("/app/src/cache.ts")
EXPECTED = """export class Store<T> {
  private items: Map<string, T> = new Map<string, T>();

  get(key: string): T | undefined {
    return this.items.get(key);
  }

  has(key: string): boolean {
    return this.items.has(key) && this.items.get(key) !== undefined;
  }

  keep(key: string, value: T): void {
    if (key.length > 0 && value !== undefined) {
      this.items.set(key, value);
    }
  }
}
"""
# JSON escapes for <, >, and &. A result that holds one shows the model text
# that does not match the file it then edits.
ESCAPES = ("\\u003c", "\\u003e", "\\u0026")

failures = []
if not TARGET.is_file() or TARGET.read_text() != EXPECTED:
    failures.append("cache.ts does not hold exactly the renamed class")

if peenaudit and peenaudit.has_frames():
    trial = peenaudit.Trial("/peen")
    escaped = [
        result["name"]
        for result in trial.tool_results().values()
        if any(escape in result["content"] for escape in ESCAPES)
    ]
    if escaped:
        failures.append(f"tool results wrote markup as JSON escapes: {escaped}")
    refused = [
        call["name"]
        for call in trial.calls_named("editFile")
        if trial.tool_results().get(call["callId"], {}).get("isError")
    ]
    if refused:
        failures.append(f"{len(refused)} editFile calls failed")

for failure in failures:
    print("FAIL:", failure)
pathlib.Path("/logs/verifier/reward.txt").write_text("0\n" if failures else "1\n")
