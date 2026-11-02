#!/bin/bash
# Add Divide following the project's Go rule.
set -euo pipefail

cat >>/app/calc/calc.go <<'EOF'

// Divide returns the integer quotient of a and b.
func Divide(a, b int) (int, error) {
	if b == 0 {
		return 0, errors.New("calc: division by zero")
	}

	return a / b, nil
}
EOF
