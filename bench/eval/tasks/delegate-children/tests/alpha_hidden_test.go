package alpha

import "testing"

func TestHiddenReverse(t *testing.T) {
	testCases := map[string]string{
		"":      "",
		"a":     "a",
		"abc":   "cba",
		"héllo": "olléh",
		"日本語":   "語本日",
	}

	for input, want := range testCases {
		if got := Reverse(input); got != want {
			t.Errorf("Reverse(%q) = %q, want %q", input, got, want)
		}
	}
}
