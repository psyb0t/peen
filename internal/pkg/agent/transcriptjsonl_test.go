package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizePathSegment(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "ordinary name", input: "child-agent", want: true},
		{name: "empty", input: "", want: false},
		{name: "whitespace only", input: "   ", want: false},
		{name: "dot", input: ".", want: false},
		{name: "dot dot", input: "..", want: false},
		{name: "embedded dot dot slash", input: "../escape", want: false},
		{name: "forward slash", input: "a/b", want: false},
		{name: "backslash", input: `a\b`, want: false},
		{name: "absolute path", input: "/etc/passwd", want: false},
		{name: "embedded null byte", input: "name\x00evil", want: false},
		{
			name:  "unicode letters are safe",
			input: "café-agent",
			want:  true,
		},
		{
			// A fullwidth solidus LOOKS like a slash to a human but is not
			// the ASCII separator either the OS or filepath.Join treats
			// specially, so it never lets a name escape its directory.
			name:  "unicode lookalike separator is not a real separator",
			input: "name／notaslash",
			want:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, ok := sanitizePathSegment(tc.input)
			assert.Equal(t, tc.want, ok)
		})
	}
}

func TestNewAgentRunTranscriptWritesAtDocumentedPath(t *testing.T) {
	t.Parallel()

	configDirectory := t.TempDir()
	sessionID := uuid.New()
	runID := uuid.New()
	ctx := context.Background()

	transcript := newAgentRunTranscript(
		ctx,
		configDirectory,
		sessionID,
		"child-agent",
		runID,
	)

	transcript.Append(ctx, transcriptLine{
		Sequence: 1,
		RunID:    runID,
		Type:     "text.delta",
		Payload:  json.RawMessage(`{"text":"a"}`),
	})
	transcript.Append(ctx, transcriptLine{
		Sequence: 2,
		RunID:    runID,
		Type:     "text.delta",
		Payload:  json.RawMessage(`{"text":"b"}`),
	})

	wantPath := filepath.Join(
		configDirectory,
		"transcripts",
		sessionID.String(),
		"agents",
		"child-agent",
		runID.String()+".jsonl",
	)

	lines := readJSONLLines(t, wantPath)
	require.Len(t, lines, 2)

	first := transcriptLine{}
	require.NoError(t, json.Unmarshal(lines[0], &first))
	assert.Equal(t, 1, first.Sequence)

	second := transcriptLine{}
	require.NoError(t, json.Unmarshal(lines[1], &second))
	assert.Equal(t, 2, second.Sequence, "lines must append in order")
}

func TestNewAgentRunTranscriptEmptyConfigDirectoryDisablesMirror(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	transcript := newAgentRunTranscript(ctx, "", uuid.New(), "child", uuid.New())

	require.NotPanics(t, func() {
		transcript.Append(ctx, transcriptLine{Type: "text.delta"})
	})
}

// Every path-escape attempt on the agent name must disable the mirror for
// that run rather than write outside the session's own directory.
func TestNewAgentRunTranscriptPathEscapeDisablesMirror(t *testing.T) {
	t.Parallel()

	maliciousNames := []string{
		"../../../etc/passwd",
		"..",
		".",
		"",
		"a/b",
		`a\b`,
		"/etc/passwd",
		"name\x00evil",
	}

	for _, name := range maliciousNames {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			configDirectory := t.TempDir()
			ctx := context.Background()
			sessionID := uuid.New()
			runID := uuid.New()

			transcript := newAgentRunTranscript(
				ctx,
				configDirectory,
				sessionID,
				name,
				runID,
			)
			assert.Empty(
				t,
				transcript.path,
				"an unsafe agent name must disable the mirror",
			)

			transcript.Append(ctx, transcriptLine{Type: "text.delta"})

			assertNoFilesOutside(
				t,
				configDirectory,
				filepath.Join(configDirectory, transcriptsDirectoryName),
			)
		})
	}
}

// A mirror write failure is logged and degrades only the mirror: it must
// never panic or otherwise surface to the caller.
func TestAgentRunTranscriptWriteFailureIsNotFatal(t *testing.T) {
	t.Parallel()

	configDirectory := t.TempDir()
	ctx := context.Background()
	sessionID := uuid.New()
	runID := uuid.New()

	transcript := newAgentRunTranscript(
		ctx,
		configDirectory,
		sessionID,
		"child-agent",
		runID,
	)
	require.NotEmpty(t, transcript.path)

	// Pre-create the exact target file path as a directory, so the real
	// open-for-append call fails.
	require.NoError(t, os.MkdirAll(transcript.path, 0o700))

	require.NotPanics(t, func() {
		transcript.Append(ctx, transcriptLine{Type: "text.delta"})
	})
}

func readJSONLLines(t *testing.T, path string) [][]byte {
	t.Helper()

	file, err := os.Open(path)
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, file.Close()) })

	lines := make([][]byte, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		lines = append(lines, line)
	}

	require.NoError(t, scanner.Err())

	return lines
}

// sanitizePathSegment is a deny-list, and deny-lists are where escapes hide.
// This is the adversarial corpus beyond the obvious `..` and `/`: encoded
// separators, mixed traversal, control characters, and names that are only
// dots. Anything that survives must still be a single harmless segment.
func TestSanitizePathSegmentAdversarialCorpus(t *testing.T) {
	t.Parallel()

	mustReject := []string{
		"..",
		".",
		"",
		"   ",
		" .. ",
		"../..",
		"..\\..",
		"a/../../b",
		"/absolute",
		"/",
		"\\",
		"C:\\windows",
		"dir/sub",
		"trailing/",
		"/leading",
		"nul\x00byte",
		"\x00",
	}

	for _, name := range mustReject {
		t.Run("reject "+name, func(t *testing.T) {
			t.Parallel()

			_, ok := sanitizePathSegment(name)
			assert.False(t, ok, "%q must not become a path segment", name)
		})
	}

	// These are odd but genuinely contained: no separator, no traversal.
	// They must survive as one segment rather than being over-rejected.
	mustAccept := []string{
		"...",
		"....",
		"incident-responder",
		"agent.with.dots",
		"agent_with_underscores",
		"Ünïcøde",
		"名前", //nolint:gosmopolitan // non-ASCII text is the point of this test
	}

	for _, name := range mustAccept {
		t.Run("accept "+name, func(t *testing.T) {
			t.Parallel()

			segment, ok := sanitizePathSegment(name)
			require.True(t, ok, "%q is contained and should be allowed", name)
			assert.NotContains(t, segment, string(filepath.Separator))
			assert.NotEqual(t, "..", segment)
			assert.NotEqual(t, ".", segment)
		})
	}
}

// Whatever sanitizePathSegment lets through, the resulting file must still
// land inside the session's own transcripts directory. This checks the
// composition, not just the sanitizer in isolation.
func TestAgentRunTranscriptNeverEscapesEvenWhenAccepted(t *testing.T) {
	t.Parallel()

	names := []string{"...", "....", "agent.with.dots", "Ünïcøde", "名前"} //nolint:gosmopolitan // non-ASCII text is the point of this test

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			configDirectory := t.TempDir()
			ctx := context.Background()

			transcript := newAgentRunTranscript(
				ctx,
				configDirectory,
				uuid.New(),
				name,
				uuid.New(),
			)
			transcript.Append(ctx, transcriptLine{Type: "text.delta"})

			assertNoFilesOutside(
				t,
				configDirectory,
				filepath.Join(configDirectory, transcriptsDirectoryName),
			)
		})
	}
}

// The durable JSONL mirror must be readable back, sharing the ring buffer's
// own cursor semantics, so a caller can follow a run across the boundary
// between "still buffered" and "durable only" without a special case.
func TestReadAgentRunTranscriptRoundTripsWrittenEvents(t *testing.T) {
	t.Parallel()

	configDirectory := t.TempDir()
	sessionID := uuid.New()
	runID := uuid.New()
	ctx := context.Background()

	transcript := newAgentRunTranscript(
		ctx, configDirectory, sessionID, "child-agent", runID,
	)
	require.NotEmpty(t, transcript.path)

	for i := 1; i <= 3; i++ {
		transcript.Append(ctx, transcriptLine{
			Sequence: i,
			RunID:    runID,
			Type:     "text.delta",
			Payload:  json.RawMessage(`{}`),
		})
	}

	events, next, err := readAgentRunTranscript(ctx, transcript.path, 0, 0)
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, 1, events[0].Sequence)
	assert.Equal(t, 3, events[2].Sequence)
	assert.Equal(t, 4, next, "next cursor resumes one past the last event")
}

// A cursor mid-stream and a bounded limit must behave exactly like the
// in-memory ring buffer's Read: skip already-seen sequences, cap the
// returned count, and report the next cursor to resume from.
func TestReadAgentRunTranscriptCursorAndLimitWindowing(t *testing.T) {
	t.Parallel()

	configDirectory := t.TempDir()
	sessionID := uuid.New()
	runID := uuid.New()
	ctx := context.Background()

	transcript := newAgentRunTranscript(
		ctx, configDirectory, sessionID, "child-agent", runID,
	)

	for i := 1; i <= 5; i++ {
		transcript.Append(ctx, transcriptLine{
			Sequence: i,
			RunID:    runID,
			Type:     "text.delta",
			Payload:  json.RawMessage(`{}`),
		})
	}

	events, next, err := readAgentRunTranscript(ctx, transcript.path, 3, 1)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, 3, events[0].Sequence)
	assert.Equal(t, 4, next)

	// A cursor past the last written event returns nothing and echoes the
	// cursor back unchanged, matching agentRunEventBuffer.Read.
	events, next, err = readAgentRunTranscript(ctx, transcript.path, 99, 10)
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.Equal(t, 99, next)
}

// A missing file must report the same sentinel this package already uses
// for a missing agent run, not a generic filesystem error.
func TestReadAgentRunTranscriptMissingFileReportsNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	missingPath := filepath.Join(t.TempDir(), "does-not-exist.jsonl")

	_, _, err := readAgentRunTranscript(ctx, missingPath, 0, 0)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

// findAgentRunTranscriptPath is the fallback used when a run's in-memory
// registry entry is gone (most commonly a service restart) and its agent
// name is therefore unknown; it must still locate the file the writer
// produced, by run ID alone.
func TestFindAgentRunTranscriptPathLocatesByRunIDAlone(t *testing.T) {
	t.Parallel()

	configDirectory := t.TempDir()
	sessionID := uuid.New()
	runID := uuid.New()
	ctx := context.Background()

	transcript := newAgentRunTranscript(
		ctx, configDirectory, sessionID, "child-agent", runID,
	)
	transcript.Append(ctx, transcriptLine{Type: "text.delta"})

	path, ok := findAgentRunTranscriptPath(configDirectory, sessionID, runID)
	require.True(t, ok)
	assert.Equal(t, transcript.path, path)
}

func TestFindAgentRunTranscriptPathReportsNotFoundCases(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		useEmptyDir bool
	}{
		{name: "empty config directory"},
		{name: "no run was ever written", useEmptyDir: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			configDirectory := ""
			if tc.useEmptyDir {
				configDirectory = t.TempDir()
			}

			_, ok := findAgentRunTranscriptPath(
				configDirectory, uuid.New(), uuid.New(),
			)
			assert.False(t, ok)
		})
	}
}

// assertNoFilesOutside walks root and fails if any regular file exists
// outside allowed.
func assertNoFilesOutside(t *testing.T, root, allowed string) {
	t.Helper()

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		assert.True(
			t,
			pathContains(allowed, filepath.Clean(path)),
			"unexpected file written outside the allowed directory: %s",
			path,
		)

		return nil
	})
	require.NoError(t, err)
}
