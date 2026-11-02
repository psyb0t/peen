package calc

import (
	"strings"
	"testing"
)

func TestHiddenDivide(t *testing.T) {
	got, err := Divide(7, 2)
	if err != nil || got != 3 {
		t.Fatalf("Divide(7, 2) = %d, %v, want 3, nil", got, err)
	}

	got, err = Divide(-9, 3)
	if err != nil || got != -3 {
		t.Fatalf("Divide(-9, 3) = %d, %v, want -3, nil", got, err)
	}
}

func TestHiddenDivideByZeroFollowsTheErrorRule(t *testing.T) {
	_, err := Divide(1, 0)
	if err == nil {
		t.Fatal("Divide(1, 0) returned no error")
	}

	if !strings.HasPrefix(err.Error(), "calc: ") {
		t.Fatalf("error %q does not start with the calc: prefix", err)
	}
}
