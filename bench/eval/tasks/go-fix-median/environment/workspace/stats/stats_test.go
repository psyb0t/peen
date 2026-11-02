package stats

import "testing"

func TestMedianOddLength(t *testing.T) {
	got := Median([]float64{3, 1, 2})
	if got != 2 {
		t.Fatalf("Median = %v, want 2", got)
	}
}
