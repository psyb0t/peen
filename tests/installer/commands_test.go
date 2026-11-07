//go:build installer

package installer_test

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigCommands(t *testing.T) {
	instance := newInstall(t, installOptions{})
	outcome := instance.installer(t, append(instance.setupFlags(dockerSpace, controlSpace), "--rolling")...)
	require.Zero(t, outcome.code, outcome.output())
	assert.Equal(t, suite.imageReference(rollingTag), instance.readEnv(t)["PEEN_IMAGE"])
	assert.Contains(t, requireManagedWrapper(t, instance.wrapper()), releaseMarkerPrefix+rollingRef)
	instance.configure(t)

	t.Run("get and path", func(t *testing.T) {
		assert.Equal(t, dockerSpace+"\n", instance.mustPeen(t, "config", "get", "PEEN_CONTROL_SPACE").stdout)
		assert.Equal(t, instance.envFile()+"\n", instance.mustPeen(t, "config", "path").stdout)
		missing := instance.peen(t, "config", "get", "ITEST_NEVER_SET")
		assert.NotZero(t, missing.code)
	})

	t.Run("set keeps one line and the value verbatim", func(t *testing.T) {
		value := `a|b&c\d"e'f$g=h {"x":1}`
		instance.mustPeen(t, "config", "set", "ITEST_PLAIN", "first")
		instance.mustPeen(t, "config", "set", "ITEST_PLAIN", value)

		assert.Equal(t, value+"\n", instance.mustPeen(t, "config", "get", "ITEST_PLAIN").stdout)
		assert.Equal(t, value, instance.readEnv(t)["ITEST_PLAIN"])
		content, err := os.ReadFile(instance.envFile())
		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(string(content), "\nITEST_PLAIN="))
		assert.Equal(t, os.FileMode(0o600), fileMode(t, instance.envFile()))
	})

	t.Run("show masks every secret", func(t *testing.T) {
		instance.mustPeen(t, "config", "set", "ITEST_SECRET_TOKEN", "s3cret-value")

		shown := instance.mustPeen(t, "config", "show").stdout

		for _, secret := range []string{providerKey, apiToken, "s3cret-value"} {
			assert.NotContains(t, shown, secret)
		}
		for _, key := range []string{providerKeyName, "PEEN_API_TOKEN", "ITEST_SECRET_TOKEN"} {
			assert.Contains(t, shown, "\n"+key+"="+maskedValue+"\n")
		}
		assert.Contains(t, shown, "\nPEEN_CONTROL_SPACE="+dockerSpace+"\n")
	})

	t.Run("set refuses bad input and leaves the file alone", func(t *testing.T) {
		before, err := os.ReadFile(instance.envFile())
		require.NoError(t, err)
		testCases := []struct {
			arguments []string
			want      string
		}{
			{arguments: []string{"1BAD", "x"}, want: "invalid setting name"},
			{arguments: []string{"BAD-KEY", "x"}, want: "invalid setting name"},
			{arguments: []string{"ITEST_LINES", "a\nb"}, want: "must be a single line"},
			{arguments: []string{"ITEST_LINES", "a\rb"}, want: "must be a single line"},
			{arguments: []string{"PEEN_CONTROL_SPACE", hostSpace}, want: "peen setup --control-space"},
			{arguments: []string{"PEEN_IMAGE", "other:tag"}, want: "peen upgrade"},
			{arguments: []string{"ONLY_KEY"}, want: "usage: peen config set"},
		}
		for _, tc := range testCases {
			outcome := instance.peen(t, append([]string{"config", "set"}, tc.arguments...)...)

			assert.NotZero(t, outcome.code, tc.arguments)
			assert.Contains(t, outcome.stderr, tc.want, tc.arguments)
		}
		after, err := os.ReadFile(instance.envFile())
		require.NoError(t, err)
		assert.Equal(t, before, after)
		assert.Equal(t, os.FileMode(0o600), fileMode(t, instance.envFile()))
	})
}

func TestDoctor(t *testing.T) {
	instance := newInstall(t, installOptions{})
	instance.mustInstall(t, dockerSpace, controlSpace)

	fresh := instance.peen(t, "doctor")
	assert.Equal(t, 1, fresh.code)
	assert.Contains(t, fresh.stdout, "FAIL  provider key")
	assert.Contains(t, fresh.stdout, "FAIL  the controller is not running")
	assert.Contains(t, fresh.stdout, "ok    "+instance.envFile()+" has mode 600")

	instance.configure(t)
	instance.mustPeen(t, "start")
	waitHealthy(t, instance)

	healthy := instance.peen(t, "doctor")
	assert.Zero(t, healthy.code, healthy.stdout)
	assert.Contains(t, healthy.stdout, "all checks passed")
	assert.Contains(t, healthy.stdout, "ok    GET /api/v1/models answers")
	assert.NotContains(t, healthy.stdout, "FAIL")

	instance.mustPeen(t, "stop")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", instance.listen())
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	require.NoError(t, os.Chmod(instance.envFile(), 0o644)) //nolint:gosec // The test loosens it on purpose.

	broken := instance.peen(t, "doctor")

	assert.Equal(t, 1, broken.code)
	assert.Contains(t, broken.stdout, "is already in use")
	assert.Contains(t, broken.stdout, "has mode 644, want 600")
}
