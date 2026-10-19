package contentblob

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errStoreOffline = errors.New("store offline")

type testMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	Reasoning  string `json:"reasoning,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
}

type testConversation struct {
	SystemPrompt string          `json:"systemPrompt"`
	Messages     []testMessage   `json:"messages"`
	Arguments    json.RawMessage `json:"arguments,omitempty"`
}

func longText(seed string) string {
	return strings.Repeat(seed+" <tag> & \"quoted\" \\ é\n", 40)
}

func memoryFetch(stored map[string][]byte) FetchFunc {
	return func(hashes []string) (map[string][]byte, error) {
		found := map[string][]byte{}
		for _, hash := range hashes {
			if data, ok := stored[hash]; ok {
				found[hash] = data
			}
		}

		return found, nil
	}
}

func storeBlobs(stored map[string][]byte, blobs []Blob) {
	for _, blob := range blobs {
		stored[blob.Hash] = blob.Data
	}
}

func encode(t *testing.T, value any) []byte {
	t.Helper()

	encoded, err := json.Marshal(value)
	require.NoError(t, err)

	return encoded
}

func roundTrip(t *testing.T, raw []byte) ([]byte, map[string][]byte) {
	t.Helper()

	compacted, blobs := Compact(raw)
	require.True(t, json.Valid(compacted), "a compacted document is JSON")

	stored := map[string][]byte{}
	storeBlobs(stored, blobs)

	for _, blob := range blobs {
		require.True(t, ValidBlob(blob.Hash, blob.Data))
	}

	expanded, err := Expand(compacted, memoryFetch(stored))
	require.NoError(t, err)
	require.Equal(t, string(raw), string(expanded))

	return compacted, stored
}

func shortMessages(count int) []testMessage {
	messages := make([]testMessage, 0, count)
	for index := range count {
		messages = append(messages, testMessage{
			Role: "tool", Content: fmt.Sprintf("ok %d", index),
			ToolCallID: fmt.Sprintf("call_%d", index),
		})
	}

	return messages
}

func TestCompactRoundTripsByteForByte(t *testing.T) {
	t.Parallel()

	raw := encode(t, testConversation{
		SystemPrompt: longText("system"),
		Messages: append(shortMessages(30),
			testMessage{
				Role: "assistant", Content: longText("answer"),
				Reasoning: longText("thinking"),
			}),
		Arguments: json.RawMessage(`{"path":"a.txt",  "body":` +
			string(encode(t, longText("file body"))) + `}`),
	})

	compacted, _ := roundTrip(t, raw)
	assert.True(t, IsCompacted(compacted))
	assert.Less(t, len(compacted), len(raw)/4)
}

func TestCompactKeepsWhitespaceAndNestingExactly(t *testing.T) {
	t.Parallel()

	value := map[string]any{
		"list":   shortMessages(40),
		"nested": [][]string{{longText("a"), "b"}, {}, {longText("c")}},
		"empty":  map[string]any{},
		"number": 1.5e3, "flag": true, "nothing": nil,
	}

	pretty, err := json.MarshalIndent(value, "  ", "\t")
	require.NoError(t, err)

	padded := append(append([]byte(" \n"), pretty...), " \t\n"...)

	compacted, _ := roundTrip(t, padded)
	assert.True(t, IsCompacted(compacted))
}

func TestCompactStoresARepeatedPartOnce(t *testing.T) {
	t.Parallel()

	text := longText("repeated")
	raw := encode(t, []string{text, text, text})

	compacted, blobs := Compact(raw)
	require.Len(t, blobs, 1)
	assert.Equal(t, []string{blobs[0].Hash}, References(compacted))
}

func TestCompactKeepsObjectKeysInline(t *testing.T) {
	t.Parallel()

	key := strings.Repeat("k", MinBlobBytes)
	raw := []byte(`{"` + key + `" : "value"}`)

	compacted, blobs := Compact(raw)
	assert.Empty(t, blobs)
	assert.Equal(t, string(raw), string(compacted))
}

func TestCompactLeavesDocumentsAlone(t *testing.T) {
	t.Parallel()

	long := longText("body")
	marker := string(markerText)

	testCases := []struct {
		name string
		raw  []byte
	}{
		{name: "short document", raw: []byte(`{"a":"b"}`)},
		{name: "nothing long", raw: encode(t, map[string]string{
			"a": strings.Repeat("x", 300), "b": strings.Repeat("y", 300),
		})},
		{name: "unterminated string", raw: []byte(`{"a":"` + long)},
		{name: "not JSON", raw: []byte(`[` + strings.Repeat(`tru,`, 200) + `]`)},
		{name: "marker text inside data", raw: encode(t,
			map[string]string{"a": long + marker})},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			compacted, blobs := Compact(tc.raw)
			assert.Empty(t, blobs)
			assert.Equal(t, string(tc.raw), string(compacted))
			assert.False(t, IsCompacted(compacted))
		})
	}
}

func TestCompactDoesNotCompactTwice(t *testing.T) {
	t.Parallel()

	compacted, blobs := Compact(encode(t, longText("once")))
	require.NotEmpty(t, blobs)

	again, more := Compact(compacted)
	assert.Empty(t, more)
	assert.Equal(t, string(compacted), string(again))
}

func TestExpandPassesUncompactedDocumentsThrough(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"plain":true}`)

	expanded, err := Expand(raw, func([]string) (map[string][]byte, error) {
		t.Fatal("an uncompacted document must not fetch")

		return map[string][]byte{}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, string(raw), string(expanded))
}

// Expand trusts its fetcher for blob bytes; CachedFetch is what checks them.
// A blob that is missing, or that a checking fetcher dropped as forged, is an
// error.
func TestExpandRefusesMissingOrForgedBlobs(t *testing.T) {
	t.Parallel()

	compacted, blobs := Compact(encode(t, longText("guarded")))
	require.Len(t, blobs, 1)

	_, err := Expand(compacted, memoryFetch(map[string][]byte{}))
	require.ErrorIs(t, err, commerr.ErrNotFound)

	forged := map[string][]byte{blobs[0].Hash: []byte(`"something else"`)}
	_, err = Expand(compacted,
		CachedFetch(NewCache(DefaultCacheBytes), memoryFetch(forged)))
	require.ErrorIs(t, err, commerr.ErrNotFound)

	_, err = Expand(compacted, func([]string) (map[string][]byte, error) {
		return nil, errStoreOffline
	})
	require.ErrorIs(t, err, errStoreOffline)
}

func TestExpandFollowsNestedReferences(t *testing.T) {
	t.Parallel()

	messages := make([]testMessage, 0, 64)
	for index := range cap(messages) {
		messages = append(messages, testMessage{
			Role: "tool", Content: longText(fmt.Sprintf("result %d", index)),
		})
	}

	compacted, stored := roundTrip(t, encode(t, messages))

	direct := References(compacted)
	assert.Less(t, len(direct), len(stored),
		"some references live inside other blobs")
}

func TestValidBlobAcceptsOneValueOrAnElementRun(t *testing.T) {
	t.Parallel()

	for _, data := range []string{
		`"text"`, `{"a":1}`, `1, 2 ,3`, `"a",{"b":[true]}`, `null`,
	} {
		assert.True(t, ValidBlob(hashOf([]byte(data)), []byte(data)), data)
	}

	for _, data := range []string{
		``, `"a" "b"`, `"open`, `1],[2`, `,1`, `1,`, `}`,
	} {
		assert.False(t, ValidBlob(hashOf([]byte(data)), []byte(data)), data)
	}

	assert.False(t, ValidBlob(hashOf([]byte(`"a"`)), []byte(`"b"`)),
		"the bytes must match the hash")
}

// A message history gains a few messages per round, and every saved step
// carries all of it. The runs of messages that did not change must be shared,
// so one compacted copy stays small however long the history grows.
func TestCompactKeepsAGrowingHistoryCopyBounded(t *testing.T) {
	t.Parallel()

	const rounds = 120

	conversation := testConversation{SystemPrompt: longText("system")}
	stored := map[string][]byte{}

	var (
		inline     int
		blobBytes  int
		lastCopy   int
		largestRef int
	)

	for round := range rounds {
		conversation.Messages = append(conversation.Messages,
			testMessage{
				Role:       "assistant",
				Content:    fmt.Sprintf("calling tool %d", round),
				ToolCallID: fmt.Sprintf("call_%d", round),
			},
			testMessage{
				Role: "tool",
				Content: fmt.Sprintf("result %d: %s", round,
					strings.Repeat("r", round%7*100)),
			},
		)

		raw := encode(t, conversation)
		compacted, blobs := Compact(raw)

		for _, blob := range blobs {
			if _, exists := stored[blob.Hash]; !exists {
				blobBytes += len(blob.Data)
			}
		}

		storeBlobs(stored, blobs)

		expanded, err := Expand(compacted, memoryFetch(stored))
		require.NoError(t, err)
		require.Equal(t, string(raw), string(expanded))

		inline += len(raw)
		lastCopy = len(compacted)
		largestRef = max(largestRef, len(References(compacted)))
	}

	assert.Less(t, lastCopy, 6<<10,
		"a copy of a long history must stay a few kilobytes")
	assert.Less(t, lastCopy+blobBytes, inline/20,
		"all copies and their blobs must be a small part of the inline size")
	assert.Less(t, largestRef, 2*rounds/runBoundaryModulus,
		"runs, not single messages, are referenced")
}
