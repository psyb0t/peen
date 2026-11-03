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
}

func TestHiddenErrorsCarryThePackagePrefix(t *testing.T) {
	_, divideErr := Divide(1, 0)
	if divideErr == nil {
		t.Fatal("Divide(1, 0) returned no error")
	}

	_, rootErr := Root(-1)
	if rootErr == nil {
		t.Fatal("Root(-1) returned no error")
	}

	for _, err := range []error{divideErr, rootErr} {
		if !strings.HasPrefix(err.Error(), "calc: ") {
			t.Fatalf("error %q does not start with the calc: prefix", err)
		}
	}
}
