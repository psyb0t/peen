package size

import "testing"

func TestHiddenParseSizeValid(t *testing.T) {
	testCases := []struct {
		input string
		want  int64
	}{
		{"0", 0},
		{"512", 512},
		{"512B", 512},
		{"512 b", 512},
		{"10 KB", 10000},
		{"10kb", 10000},
		{"3MiB", 3145728},
		{"3 mib", 3145728},
		{"1gb", 1000000000},
		{"2GiB", 2147483648},
		{"  7 KiB  ", 7168},
		{"9223372036854775807", 9223372036854775807},
	}

	for _, tc := range testCases {
		got, err := ParseSize(tc.input)
		if err != nil {
			t.Errorf("ParseSize(%q) error = %v", tc.input, err)

			continue
		}

		if got != tc.want {
			t.Errorf("ParseSize(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestHiddenParseSizeInvalid(t *testing.T) {
	for _, input := range []string{
		"",
		"   ",
		"-5",
		"-5MB",
		"1.5MB",
		"10 TB",
		"10XB",
		"MB",
		"10  MB",
		"9223372036854775807KB",
		"99999999999GiB",
		"12abc",
	} {
		if _, err := ParseSize(input); err == nil {
			t.Errorf("ParseSize(%q) returned no error", input)
		}
	}
}
