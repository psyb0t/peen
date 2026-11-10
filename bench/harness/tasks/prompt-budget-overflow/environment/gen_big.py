"""Write one AGENTS.md far over the system prompt budget. Deterministic."""

import sys

with open(sys.argv[1], "w") as handle:
    handle.write("# big\n\n")
    for number in range(1300):
        handle.write(
            f"Convention {number:04d}: keep item {number:04d} tidy, documented, and reviewed before release.\n"
        )
