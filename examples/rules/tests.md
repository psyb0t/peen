---
paths:
  - "**/*_test.go"
  - "**/test_*.py"
  - "**/*_test.py"
  - "**/*.{test,spec}.{ts,tsx,js,jsx,mjs,cjs}"
  - "**/tests/**"
---

# Tests

Sources: the unit testing chapters of [Software Engineering at Google](https://abseil.io/resources/swe-book) (Winters, Manshreck, and Wright).

Follow the project's test framework, helpers, and the shape of its existing tests first. Use this rule where the project says nothing.

- Test behavior through the public interface of the code, not its private details, so a refactor does not break the test.
- Cover one behavior per test, and name the test after the scenario and the expected result.
- Keep tests deterministic. No sleeps to wait for timing, no real network unless the test is meant to use it, no dependence on test order, and a fixed clock and seed where time or randomness matter.
- Cover the error paths and edge cases of the code you change, not only the success path.
- Prefer clear, repeated setup in each test over shared helpers that hide what the test does.
- Never delete, skip, or loosen a failing test to make it pass. Fix the code, or report the failure.
- Run the project's test command and report what it printed.
