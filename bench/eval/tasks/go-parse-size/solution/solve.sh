#!/bin/bash
# Write a ParseSize that follows SPEC.md.
set -euo pipefail

cat >/app/size/size.go <<'EOF'
// Package size parses human-written byte sizes.
package size

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

var errInvalid = errors.New("size: invalid size")

var multipliers = map[string]int64{
	"":    1,
	"b":   1,
	"kb":  1000,
	"mb":  1000 * 1000,
	"gb":  1000 * 1000 * 1000,
	"kib": 1024,
	"mib": 1024 * 1024,
	"gib": 1024 * 1024 * 1024,
}

// ParseSize returns the number of bytes s describes. See SPEC.md.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)

	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}

	if end == 0 {
		return 0, errInvalid
	}

	number, err := strconv.ParseInt(s[:end], 10, 64)
	if err != nil {
		return 0, errInvalid
	}

	unit := s[end:]
	unit = strings.TrimPrefix(unit, " ")

	multiplier, known := multipliers[strings.ToLower(unit)]
	if !known || strings.HasPrefix(unit, " ") {
		return 0, errInvalid
	}

	if number > math.MaxInt64/multiplier {
		return 0, errInvalid
	}

	return number * multiplier, nil
}
EOF
