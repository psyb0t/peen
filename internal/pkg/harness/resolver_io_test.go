package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testFirstFileName  = "a.txt"
	testSecondFileName = "b.txt"
	testThirdFileName  = "c.txt"
	testLastFileName   = "z.txt"
)

func TestReadLimitedEnforcesByteBoundary(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		content      string
		maxBytes     int64
		wantContent  string
		wantExceeded bool
	}{
		{
			name:        "accepts exact boundary",
			content:     "abcd",
			maxBytes:    4,
			wantContent: "abcd",
		},
		{
			name:         "rejects one byte beyond boundary",
			content:      "abcde",
			maxBytes:     4,
			wantExceeded: true,
		},
		{
			name:        "accepts empty content at zero boundary",
			maxBytes:    0,
			wantContent: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			content, exceeded, err := readLimited(
				strings.NewReader(tc.content),
				tc.maxBytes,
			)
			require.NoError(t, err)
			assert.Equal(t, tc.wantExceeded, exceeded)
			assert.Equal(t, tc.wantContent, string(content))
		})
	}
}

func TestSortedDirectoryEntriesEnforcesEntryBoundary(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		fileNames  []string
		maxEntries int
		wantNames  []string
		wantErr    error
	}{
		{
			name:       "accepts exact boundary and sorts bytewise",
			fileNames:  []string{testLastFileName, testFirstFileName},
			maxEntries: 2,
			wantNames:  []string{testFirstFileName, testLastFileName},
		},
		{
			name: "rejects one irrelevant entry beyond boundary",
			fileNames: []string{
				testFirstFileName,
				testSecondFileName,
				testThirdFileName,
			},
			maxEntries: 2,
			wantErr:    ErrResourceLimit,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			directory := t.TempDir()
			for _, fileName := range tc.fileNames {
				err := os.WriteFile(
					filepath.Join(directory, fileName),
					[]byte("fixture"),
					testFileMode,
				)
				require.NoError(t, err)
			}

			entries, err := sortedDirectoryEntries(directory, tc.maxEntries)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)

			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}

			assert.Equal(t, tc.wantNames, names)
		})
	}
}

func TestCloseFilePreservesEarlierError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, os.WriteFile(path, []byte("fixture"), testFileMode))
	file, err := os.Open(path)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	earlierError := ErrInvalidOptions
	err = closeFile(file, earlierError)
	require.ErrorIs(t, err, earlierError)
}
