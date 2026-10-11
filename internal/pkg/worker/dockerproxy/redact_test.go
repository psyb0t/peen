package dockerproxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactInspectEnv(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "values replaced and names kept",
			input: `{"Config":{"Env":["A=1","B=x=y","C"]}}`,
			want:  `{"Config":{"Env":["A=<redacted>","B=<redacted>","C"]}}`,
		},
		{
			name:  "container config redacted too",
			input: `{"Config":{"Env":[]},"ContainerConfig":{"Env":["A=1"]}}`,
			want:  `{"Config":{"Env":[]},"ContainerConfig":{"Env":["A=<redacted>"]}}`,
		},
		{
			name:  "null environment left alone",
			input: `{"Config":{"Env":null,"Image":"x"}}`,
			want:  `{"Config":{"Env":null,"Image":"x"}}`,
		},
		{
			name:  "configuration without an environment",
			input: `{"Config":{"Image":"x"}}`,
			want:  `{"Config":{"Image":"x"}}`,
		},
		{
			name:  "null configuration",
			input: `{"Config":null,"Id":"a"}`,
			want:  `{"Config":null,"Id":"a"}`,
		},
		{
			name:  "no configuration",
			input: `{"Id":"a"}`,
			want:  `{"Id":"a"}`,
		},
		{
			name:  "html characters stay unescaped",
			input: `{"Config":{"Env":["A=<b>"],"Labels":{"k":"<&>"}}}`,
			want:  `{"Config":{"Env":["A=<redacted>"],"Labels":{"k":"<&>"}}}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := redactInspectEnv([]byte(tc.input))
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
			// An HTML-escaping encoder would write the brackets as u003c and
			// u003e escapes, so neither may appear.
			assert.NotContains(t, string(got), "u003c")
			assert.NotContains(t, string(got), "u003e")
		})
	}
}

func TestRedactInspectEnvRefusesMalformedDocuments(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		input string
	}{
		{name: "not json", input: `A=1`},
		{name: "not an object", input: `["A=1"]`},
		{name: "configuration not an object", input: `{"Config":"A=1"}`},
		{name: "environment not a list", input: `{"Config":{"Env":"A=1"}}`},
		{name: "environment entry not text", input: `{"Config":{"Env":[1]}}`},
		{
			name:  "container configuration not an object",
			input: `{"ContainerConfig":["A=1"]}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := redactInspectEnv([]byte(tc.input))
			require.Error(t, err)
		})
	}
}
