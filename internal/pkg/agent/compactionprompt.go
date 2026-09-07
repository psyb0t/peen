package agent

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

const (
	// compactionPromptFileName is the deployment override read from the
	// configuration directory at startup.
	compactionPromptFileName = "COMPACTION.md"

	// maxCompactionPromptBytes bounds the override. The prompt leads every
	// summarization call, so an unbounded file spends the summary budget on
	// its own instructions.
	maxCompactionPromptBytes = 32 * 1024
)

// defaultCompactionPrompt is the embedded summarizer instructions a deployment
// gets without a COMPACTION.md of its own. It lives in a file so its wording
// stays readable at the width it is written in, and so the default and the
// override are the same format.
//
//go:embed compaction_prompt.md
var defaultCompactionPrompt string

// LoadCompactionPrompt returns the summarizer instructions for a deployment.
//
// An absent COMPACTION.md uses the embedded default. A present one must be
// non-empty and within the byte bound, because a summarizer with no usable
// instructions produces a summary that silently replaces real history.
func LoadCompactionPrompt(configDirectory string) (string, error) {
	if configDirectory == "" {
		return defaultCompactionPrompt, nil
	}

	path := filepath.Join(configDirectory, compactionPromptFileName)

	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return defaultCompactionPrompt, nil
	}

	if err != nil {
		return "", ctxerrors.Wrap(err, "stat compaction prompt")
	}

	if !info.Mode().IsRegular() {
		return "", ctxerrors.Wrapf(
			ErrCompactionPromptInvalid,
			"%s is not a regular file",
			compactionPromptFileName,
		)
	}

	if info.Size() > maxCompactionPromptBytes {
		return "", ctxerrors.Wrapf(
			ErrCompactionPromptInvalid,
			"%s exceeds %d bytes",
			compactionPromptFileName,
			maxCompactionPromptBytes,
		)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read compaction prompt")
	}

	prompt := strings.TrimSpace(string(content))
	if prompt == "" {
		return "", ctxerrors.Wrapf(
			ErrCompactionPromptInvalid,
			"%s is empty",
			compactionPromptFileName,
		)
	}

	return prompt, nil
}

// compactionPromptHash identifies which instructions produced a stored
// summary, so a row remains inspectable after the file changes.
func compactionPromptHash(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))

	return hex.EncodeToString(sum[:])
}
