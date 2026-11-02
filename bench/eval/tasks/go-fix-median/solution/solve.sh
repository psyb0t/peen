#!/bin/bash
# Write a Median that sorts a copy and averages the two middle values.
set -euo pipefail

cat >/app/stats/stats.go <<'EOF'
// Package stats computes summary statistics.
package stats

import (
	"slices"
	"sort"
)

// Median returns the median of values. It returns 0 for an empty slice.
func Median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}

	sorted := slices.Clone(values)
	sort.Float64s(sorted)

	middle := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[middle-1] + sorted[middle]) / 2
	}

	return sorted[middle]
}
EOF
