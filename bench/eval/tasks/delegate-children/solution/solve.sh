#!/bin/bash
# Implement both packages.
set -euo pipefail

cat >/app/alpha/alpha.go <<'EOF'
// Package alpha transforms words.
package alpha

// Reverse returns s with its characters in reverse order. It reverses
// characters, not bytes, so "héllo" becomes "olléh".
func Reverse(s string) string {
	runes := []rune(s)
	for left, right := 0, len(runes)-1; left < right; left, right = left+1, right-1 {
		runes[left], runes[right] = runes[right], runes[left]
	}

	return string(runes)
}
EOF

cat >/app/beta/beta.go <<'EOF'
// Package beta checks words.
package beta

import "unicode"

// IsPalindrome reports whether s reads the same forwards and backwards,
// ignoring case and every character that is not a letter or a digit. An
// empty string is a palindrome.
func IsPalindrome(s string) bool {
	kept := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			kept = append(kept, unicode.ToLower(r))
		}
	}

	for left, right := 0, len(kept)-1; left < right; left, right = left+1, right-1 {
		if kept[left] != kept[right] {
			return false
		}
	}

	return true
}
EOF
