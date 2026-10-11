package naming

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsValid(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "single word", input: "readFile", want: true},
		{name: "plain lowercase", input: "turn", want: true},
		{name: "dotted parts", input: "userMessage.created", want: true},
		{name: "three parts", input: "agent.run.started", want: true},
		{name: "digits after a letter", input: "sha256Sum", want: true},
		{name: "empty", input: "", want: false},
		{name: "underscore inside a part", input: "read_file", want: false},
		{name: "underscore later", input: "user.message_made", want: false},
		{name: "hyphen", input: "read-file", want: false},
		{name: "uppercase first", input: "ReadFile", want: false},
		{name: "uppercase later part", input: "agent.Run", want: false},
		{name: "leading digit", input: "1file", want: false},
		{name: "empty part", input: "agent..run", want: false},
		{name: "trailing dot", input: "agent.", want: false},
		{name: "space", input: "read file", want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, IsValid(tc.input))
		})
	}
}
