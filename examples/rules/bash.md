---
paths:
  - "**/*.sh"
  - "**/*.bash"
---

# Shell scripts

Sources: the [Google Shell Style Guide](https://google.github.io/styleguide/shellguide.html) and [ShellCheck](https://www.shellcheck.net/).

Follow the project's own lint configuration and the style of the surrounding scripts first. Use this rule where the project says nothing.

- Start a script with its interpreter, `#!/usr/bin/env bash` or `#!/bin/sh`, and stop on errors with `set -euo pipefail` in Bash or `set -eu` in POSIX `sh`.
- Quote every expansion: `"$file"`, `"${files[@]}"`, `"$(command)"`.
- Use `$(...)` for command substitution, never backticks. In Bash, use `[[ ... ]]` for tests.
- Declare function variables with `local`.
- Write errors to stderr and exit non-zero on failure.
- Do not parse `ls`. Use globs, or `find ... -print0` with `xargs -0` or `while IFS= read -r -d ''`.
- Create temporary files with `mktemp` and remove them in a `trap ... EXIT`.
- Run `shellcheck` on every script you change and fix what it reports. Disable a check only with a `# shellcheck disable=SCxxxx` comment that says why.
- Format with `shfmt` when the project uses it.
