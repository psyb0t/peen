package hooks

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

const executableMode = 0o111

func (r Runner) commandActionTimeout(
	action harness.HookAction,
) (time.Duration, error) {
	timeout, err := action.CommandTimeout()
	if err != nil {
		return 0, ctxerrors.Wrap(err, "resolve hook command timeout")
	}

	if timeout == 0 {
		return r.commandTimeout, nil
	}

	return timeout, nil
}

func (r Runner) operatorCommand(
	action harness.HookAction,
) (string, map[string]string, error) {
	environment := cloneEnvironment(action.Environment)

	directory, err := (config.Config{
		ConfigDirectory: r.configDirectory,
	}).OperatorBinDirectory()
	if err != nil {
		return "", nil, ctxerrors.Wrap(err, "resolve hook command directory")
	}

	if directory == "" {
		return action.Command, environment, nil
	}

	searchPath, set := environment["PATH"]
	if !set {
		searchPath = os.Getenv("PATH")
	}

	environment["PATH"] = directory
	if searchPath != "" {
		environment["PATH"] += string(os.PathListSeparator) + searchPath
	}

	command, err := operatorExecutable(directory, action.Command)
	if err != nil {
		return "", nil, err
	}

	return command, environment, nil
}

// exec.Command resolves a bare executable before applying Cmd.Env. Resolve
// the prepended directory explicitly for both native and Commander backends.
func operatorExecutable(directory, name string) (string, error) {
	if strings.ContainsAny(name, `/\`) {
		return name, nil
	}

	candidate := filepath.Join(directory, name)

	info, err := os.Stat(candidate)
	if errors.Is(err, fs.ErrNotExist) {
		return name, nil
	}

	if err != nil {
		return "", ctxerrors.Wrap(err, "inspect operator hook executable")
	}

	if info.IsDir() || info.Mode()&executableMode == 0 {
		return name, nil
	}

	return candidate, nil
}
