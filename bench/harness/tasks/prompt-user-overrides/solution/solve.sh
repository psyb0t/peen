#!/bin/bash
# Write the test file the user named.
set -euo pipefail

cat >/app/calc_test.go <<'GO'
package calc

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("Add(2, 3) is not 5")
	}
}
GO
