// Package stats computes summary statistics.
package stats

import "sort"

// Median returns the median of values. It returns 0 for an empty slice.
func Median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}

	sort.Float64s(values)

	return values[len(values)/2]
}
