#!/bin/bash
# Fix Root, add Divide, and save the correction as a learned rule.
set -euo pipefail

sed -i 's/errors.New("negative input")/errors.New("calc: negative input")/' /app/calc/calc.go

cat >>/app/calc/calc.go <<'EOF'

// Divide returns the integer quotient of a and b.
func Divide(a, b int) (int, error) {
	if b == 0 {
		return 0, errors.New("calc: division by zero")
	}

	return a / b, nil
}
EOF

mkdir -p /app/.agents/learned/rules
cat >/app/.agents/learned/rules/calc-errors.md <<'EOF'
---
paths:
  - "calc/**/*.go"
---
# Calc errors

- Every error message package calc returns starts with `calc: `.
EOF
