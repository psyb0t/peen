package modeljson

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalKeepsMarkupReadable(t *testing.T) {
	t.Parallel()

	value := map[string]string{"content": "func f[T any]() { a < b && c > d }"}

	encoded, err := Marshal(value)
	require.NoError(t, err)

	assert.JSONEq(t, `{"content":"func f[T any]() { a < b && c > d }"}`,
		string(encoded))
	assert.NotContains(t, string(encoded), `\`+"u003c")
	assert.NotContains(t, string(encoded), `\`+"u0026")
	assert.NotContains(t, string(encoded), "\n", "no trailing newline")

	var decoded map[string]string
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, value, decoded)
}

func TestMarshalFailsOnUnencodableValue(t *testing.T) {
	t.Parallel()

	_, err := Marshal(make(chan int))
	require.Error(t, err)
}
