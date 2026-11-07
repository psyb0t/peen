//go:build installer

package installer_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	systemUser      = "itestuser"
	systemUserID    = "2000"
	systemUserHome  = "/home/itestuser"
	systemWrapper   = "/usr/local/bin/peen"
	systemBinary    = "/usr/local/lib/peen/peen"
	systemPeenHome  = "/home/itestuser/.config/peen"
	pathWarning     = "is not on your PATH"
	notRunningYet   = "Peen is not running yet"
	hostRootRefusal = "host root"
	foreignRefusal  = "was not created by this installation"
	maskedValue     = "********"
	restartHint     = "run peen restart"
	outsideRootHint = "peen config set PEEN_WORKSPACE_ROOT "
)

var pidPattern = regexp.MustCompile(`running \(pid (\d+)`)

func TestMain(m *testing.M) {
	setupContext, cancelSetup := context.WithTimeout(context.Background(), suiteSetupTimeout)
	instance, err := setupSuite(setupContext)
	cancelSetup()
	suite = instance
	exitCode := 1
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "set up the installer suite:", err)
	} else {
		exitCode = m.Run()
	}

	teardownContext, cancelTeardown := context.WithTimeout(context.Background(), suiteTeardownTimeout)
	if err := instance.teardown(teardownContext); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "tear down the installer suite:", err)
		exitCode = 1
	}
	cancelTeardown()

	os.Exit(exitCode)
}

func canonical(t *testing.T, path string) string {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)

	return resolved
}
