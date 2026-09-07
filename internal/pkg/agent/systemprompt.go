package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

const (
	// systemPromptFileName replaces Peen's embedded default entirely.
	systemPromptFileName = "SYSTEM.md"
	// appendSystemPromptFileName adds to whichever base prompt is in force.
	appendSystemPromptFileName = "APPEND_SYSTEM.md"

	// maxSystemPromptFileBytes bounds each file. Both lead every turn's
	// system context, so an unbounded file spends the context budget before
	// the conversation starts.
	maxSystemPromptFileBytes = 64 * 1024
)

// LoadSystemPrompt assembles the deployment prompt from the configuration
// directory: the embedded default, replaced by SYSTEM.md when present, then
// extended by APPEND_SYSTEM.md when present.
//
// Both files are optional. A present one must be non-empty and within the byte
// bound, because a deployment that meant to shape the agent and instead
// silently shipped an empty file is worse than one that fails to start.
func LoadSystemPrompt(configDirectory string) (string, error) {
	base := defaultSystemPrompt

	replacement, err := readPromptFile(configDirectory, systemPromptFileName)
	if err != nil {
		return "", err
	}

	if replacement != "" {
		base = replacement
	}

	appended, err := readPromptFile(
		configDirectory,
		appendSystemPromptFileName,
	)
	if err != nil {
		return "", err
	}

	if appended == "" {
		return base, nil
	}

	return base + systemSectionGap + appended, nil
}

// readPromptFile returns the trimmed contents of one optional prompt file, or
// an empty string when the deployment did not supply it.
func readPromptFile(configDirectory string, name string) (string, error) {
	if configDirectory == "" {
		return "", nil
	}

	path := filepath.Join(configDirectory, name)

	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", ctxerrors.Wrapf(err, "stat %s", name)
	}

	if !info.Mode().IsRegular() {
		return "", ctxerrors.Wrapf(
			ErrSystemPromptInvalid,
			"%s is not a regular file",
			name,
		)
	}

	if info.Size() > maxSystemPromptFileBytes {
		return "", ctxerrors.Wrapf(
			ErrSystemPromptInvalid,
			"%s exceeds %d bytes",
			name,
			maxSystemPromptFileBytes,
		)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", ctxerrors.Wrapf(err, "read %s", name)
	}

	prompt := strings.TrimSpace(string(content))
	if prompt == "" {
		return "", ctxerrors.Wrapf(ErrSystemPromptInvalid, "%s is empty", name)
	}

	return prompt, nil
}
