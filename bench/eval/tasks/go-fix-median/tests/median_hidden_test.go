package stats

import (
	"slices"
	"testing"
)

func TestHiddenMedianEvenLength(t *testing.T) {
	if got := Median([]float64{4, 1, 3, 2}); got != 2.5 {
		t.Fatalf("Median = %v, want 2.5", got)
	}
}

func TestHiddenMedianSingle(t *testing.T) {
	if got := Median([]float64{7}); got != 7 {
		t.Fatalf("Median = %v, want 7", got)
	}
}

func TestHiddenMedianEmpty(t *testing.T) {
	if got := Median(nil); got != 0 {
		t.Fatalf("Median = %v, want 0", got)
	}
}

func TestHiddenMedianKeepsInput(t *testing.T) {
	values := []float64{5, 1, 4, 2, 3}
	original := slices.Clone(values)

	if got := Median(values); got != 3 {
		t.Fatalf("Median = %v, want 3", got)
	}

	if !slices.Equal(values, original) {
		t.Fatalf("Median changed its input to %v", values)
	}
}
