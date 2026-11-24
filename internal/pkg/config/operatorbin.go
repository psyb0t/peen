package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/psyb0t/ctxerrors"
)

// OperatorBinDirectory returns the deployment's command directory when it
// exists. It never searches the session workspace.
func (c Config) OperatorBinDirectory() (string, error) {
	if c.ConfigDirectory == "" {
		return "", nil
	}

	directory := filepath.Join(c.ConfigDirectory,
		defaultOperatorBinDirectoryPath)

	info, err := os.Stat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", ctxerrors.Wrap(err, "inspect operator command directory")
	}

	if !info.IsDir() {
		return "", nil
	}

	return directory, nil
}

func (c Config) prependOperatorBin(environ []string) ([]string, error) {
	directory, err := c.OperatorBinDirectory()
	if err != nil {
		return nil, err
	}

	if directory == "" {
		return environ, nil
	}

	return prependCommandPath(environ, directory), nil
}
