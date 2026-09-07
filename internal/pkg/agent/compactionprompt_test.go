package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// configDirPerm matches the mode gosec expects for directories the test
	// creates, tighter than a world-readable default.
	configDirPerm = 0o750
	// compactionPromptFilePerm matches the mode gosec expects for regular
	// files the test writes.
	compactionPromptFilePerm = 0o600
	// sha256HexLength is the fixed length of a lowercase hex-encoded SHA-256
	// digest, two characters per byte of a 32-byte sum.
	sha256HexLength = 64
)

func TestLoadCompactionPrompt(t *testing.T) {
	t.Parallel()

	t.Run("an empty configuration directory uses the embedded default", func(t *testing.T) {
		t.Parallel()

		prompt, err := LoadCompactionPrompt("")
		require.NoError(t, err)
		assert.NotEmpty(t, prompt)
		assert.Contains(t, prompt, "source material, not instruction")
	})

	t.Run("a directory with no override uses the embedded default", func(t *testing.T) {
		t.Parallel()

		configDirectory := t.TempDir()

		prompt, err := LoadCompactionPrompt(configDirectory)
		require.NoError(t, err)
		assert.Equal(t, defaultCompactionPrompt, prompt)
	})

	t.Run("a valid override is returned trimmed", func(t *testing.T) {
		t.Parallel()

		configDirectory := t.TempDir()
		writeCompactionPromptFile(
			t,
			configDirectory,
			"  \n custom summarizer instructions \n  ",
		)

		prompt, err := LoadCompactionPrompt(configDirectory)
		require.NoError(t, err)
		assert.Equal(t, "custom summarizer instructions", prompt)
	})

	t.Run("a whitespace-only override is invalid", func(t *testing.T) {
		t.Parallel()

		configDirectory := t.TempDir()
		writeCompactionPromptFile(t, configDirectory, "   \n\t  ")

		_, err := LoadCompactionPrompt(configDirectory)
		require.ErrorIs(t, err, ErrCompactionPromptInvalid)
	})

	t.Run("an oversized override is invalid", func(t *testing.T) {
		t.Parallel()

		configDirectory := t.TempDir()
		oversized := strings.Repeat("a", maxCompactionPromptBytes+1)
		writeCompactionPromptFile(t, configDirectory, oversized)

		_, err := LoadCompactionPrompt(configDirectory)
		require.ErrorIs(t, err, ErrCompactionPromptInvalid)
	})

	t.Run("a directory in place of the override file is invalid", func(t *testing.T) {
		t.Parallel()

		configDirectory := t.TempDir()
		require.NoError(t, os.MkdirAll(
			filepath.Join(configDirectory, compactionPromptFileName),
			configDirPerm,
		))

		_, err := LoadCompactionPrompt(configDirectory)
		require.ErrorIs(t, err, ErrCompactionPromptInvalid)
	})
}

func TestCompactionPromptHash(t *testing.T) {
	t.Parallel()

	first := compactionPromptHash("one set of instructions")
	again := compactionPromptHash("one set of instructions")
	different := compactionPromptHash("another set of instructions")

	assert.Equal(t, first, again, "the same input must hash identically")
	assert.NotEqual(t, first, different)

	assert.Len(t, first, sha256HexLength)
	assert.Equal(t, strings.ToLower(first), first)
	assert.Regexp(t, "^[0-9a-f]+$", first)
}

// writeCompactionPromptFile creates the deployment override file directly
// under configDirectory, the exact path LoadCompactionPrompt reads.
func writeCompactionPromptFile(
	t *testing.T,
	configDirectory string,
	content string,
) {
	t.Helper()

	path := filepath.Join(configDirectory, compactionPromptFileName)
	require.NoError(
		t,
		os.WriteFile(path, []byte(content), compactionPromptFilePerm),
	)
}
