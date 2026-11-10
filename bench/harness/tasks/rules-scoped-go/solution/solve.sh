#!/bin/bash
set -euo pipefail

cat >>/app/textutil/textutil.go <<'EOF'

// Reverse returns s with its runes in reverse order.
//
// Stability: experimental
func Reverse(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}
EOF
