package pathglob

import (
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatch(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{name: "star in root", pattern: "*.md", path: "README.md", want: true},
		{name: "star stays in segment", pattern: "*.md", path: "docs/a.md"},
		{name: "double star prefix at root", pattern: "**/*.go", path: "main.go", want: true},
		{name: "double star prefix nested", pattern: "**/*.go", path: "cmd/app/main.go", want: true},
		{name: "double star wrong extension", pattern: "**/*.go", path: "cmd/main.py"},
		{name: "directory then everything", pattern: "src/**/*", path: "src/a/b.ts", want: true},
		{name: "directory then everything direct", pattern: "src/**/*", path: "src/b.ts", want: true},
		{name: "trailing double star", pattern: "src/**", path: "src/a/b", want: true},
		{name: "trailing double star other dir", pattern: "src/**", path: "lib/a"},
		{name: "middle double star", pattern: "a/**/b.txt", path: "a/b.txt", want: true},
		{name: "middle double star deep", pattern: "a/**/b.txt", path: "a/x/y/b.txt", want: true},
		{name: "question mark", pattern: "file?.txt", path: "file1.txt", want: true},
		{name: "question mark not separator", pattern: "a?b", path: "a/b"},
		{name: "brace alternatives", pattern: "src/*.{ts,tsx}", path: "src/app.tsx", want: true},
		{name: "brace alternatives miss", pattern: "src/*.{ts,tsx}", path: "src/app.js"},
		{name: "nested braces", pattern: "{a,b{c,d}}.txt", path: "bd.txt", want: true},
		{name: "brace with double star", pattern: "{**/*.ts,docs/*.md}", path: "x/y.ts", want: true},
		{name: "literal single brace", pattern: "{a}.txt", path: "{a}.txt", want: true},
		{name: "bracket set", pattern: "file[0-9].txt", path: "file7.txt", want: true},
		{name: "bracket set miss", pattern: "file[0-9].txt", path: "fileA.txt"},
		{name: "negated bracket", pattern: "file[!0-9].txt", path: "fileA.txt", want: true},
		{name: "caret negated bracket", pattern: "file[^0-9].txt", path: "file1.txt"},
		{name: "negated bracket never separator", pattern: "a[!x]b", path: "a/b"},
		{name: "escaped bracket", pattern: `photos \[2024/**`, path: "photos [2024/a.jpg", want: true},
		{name: "regex metacharacters literal", pattern: "a+b(c).txt", path: "a+b(c).txt", want: true},
		{name: "dot is literal", pattern: "*.go", path: "mainxgo"},
		{name: "invalid pattern matches nothing", pattern: "photos [2024/**", path: "photos [2024/a.jpg"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, Match(tc.pattern, tc.path))
		})
	}
}

func TestValidateRejectsMalformedPatterns(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		pattern string
	}{
		{name: "unclosed bracket", pattern: "a[bc"},
		{name: "unclosed brace", pattern: "{a,b"},
		{name: "trailing escape", pattern: `a\`},
		{name: "bracket with only negation", pattern: "a[!"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, Validate(tc.pattern), commerr.ErrValidationFailed)
		})
	}
}

func TestValidateAcceptsWellFormedPatterns(t *testing.T) {
	t.Parallel()

	for _, pattern := range []string{
		"**/*.go",
		"src/**/*.{ts,tsx}",
		"[]a]",
		"a,b}",
		"",
	} {
		require.NoError(t, Validate(pattern), pattern)
	}
}
