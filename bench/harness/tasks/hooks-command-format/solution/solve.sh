#!/bin/bash
set -euo pipefail

cat >/app/hello/hello.go <<'EOF'
// Copyright Example Corp
package hello

// Hello returns the greeting.
func Hello() string {
	return "hello"
}
EOF
