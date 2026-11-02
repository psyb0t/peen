package beta

import "testing"

func TestHiddenIsPalindrome(t *testing.T) {
	testCases := map[string]bool{
		"":                               true,
		"a":                              true,
		"Racecar":                        true,
		"A man, a plan, a canal: Panama": true,
		"No 'x' in Nixon":                true,
		"12321":                          true,
		"hello":                          false,
		"ab":                             false,
	}

	for input, want := range testCases {
		if got := IsPalindrome(input); got != want {
			t.Errorf("IsPalindrome(%q) = %v, want %v", input, got, want)
		}
	}
}
