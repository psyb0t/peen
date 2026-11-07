//go:build browser

package browser_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/require"
)

func requireNetworkRequest(
	t *testing.T,
	entries []browserLog,
	method string,
	path string,
) {
	t.Helper()

	for _, entry := range entries {
		if entry.Type == browserNetworkRequest && entry.Method == method &&
			networkPath(entry.URL) == path {
			return
		}
	}

	t.Fatalf("browser did not issue expected %s request to %s", method, path)
}

func requireNetworkResponse(
	t *testing.T,
	entries []browserLog,
	path string,
	status int,
) {
	t.Helper()

	for _, entry := range entries {
		if entry.Type == browserNetworkResponse && entry.Status == status &&
			networkPath(entry.URL) == path {
			return
		}
	}

	t.Fatalf("browser did not receive expected status %d from %s", status, path)
}

func requireNoNetworkRequest(
	t *testing.T,
	entries []browserLog,
	method string,
	path string,
) {
	t.Helper()

	for _, entry := range entries {
		require.Falsef(
			t,
			entry.Type == browserNetworkRequest && entry.Method == method &&
				networkPath(entry.URL) == path,
			"browser unexpectedly issued %s to %s",
			method,
			path,
		)
	}
}

func networkPath(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	return parsed.Path
}

func requireSafeBrowserDiagnostics(t *testing.T, entries []browserLog) {
	t.Helper()

	seenDiagnostics := 0
	seenEvents := make(map[string]bool)
	for _, entry := range entries {
		require.NotContains(t, entry.Text, testinfra.TestAPIToken)
		require.NotContains(t, entry.Text, browserTestMessage)
		require.NotEqual(t, browserConsoleError, entry.Type)

		if !strings.HasPrefix(entry.Text, browserLogPrefix) {
			continue
		}

		seenDiagnostics++
		payload := map[string]any{}
		require.NoError(t, json.Unmarshal(
			[]byte(strings.TrimPrefix(entry.Text, browserLogPrefix)),
			&payload,
		))
		event, ok := payload[browserEventKey].(string)
		require.True(t, ok)
		require.NotEmpty(t, event)
		seenEvents[event] = true

		for key, value := range payload {
			require.NotContains(t, strings.ToLower(key), "token")
			require.NotContains(t, strings.ToLower(key), "message")
			require.NotContains(t, strings.ToLower(key), "body")
			require.NotContains(t, strings.ToLower(key), "content")
			require.NotContains(t, strings.ToLower(key), "data")
			require.NotContains(t, strings.ToLower(key), "error")

			switch value.(type) {
			case bool, float64, nil, string:
			default:
				t.Fatalf("browser diagnostic %q has non-primitive metadata", key)
			}
		}
	}

	require.Greater(t, seenDiagnostics, 0)
	for _, event := range expectedBrowserDiagnostics() {
		require.Truef(t, seenEvents[event], "missing browser diagnostic %q", event)
	}
}

func requireScreenshot(ctx context.Context, t *testing.T) {
	t.Helper()

	requireNamedScreenshot(ctx, t, browserScreenshotBaseName)
}

func requireNamedScreenshot(ctx context.Context, t *testing.T, name string) {
	t.Helper()

	screenshot, err := stealthyBrowser.screenshot(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, screenshot)

	configuration, err := png.DecodeConfig(bytes.NewReader(screenshot))
	require.NoError(t, err)
	require.Positive(t, configuration.Width)
	require.Positive(t, configuration.Height)
	require.LessOrEqual(t, configuration.Width, browserMaxScreenshotEdge)
	require.LessOrEqual(t, configuration.Height, browserMaxScreenshotEdge)

	artifactDirectory := t.TempDir()
	if configuredRoot := os.Getenv(browserArtifactRootEnv); configuredRoot != "" {
		//nolint:gosec // The root is the test operator's own artifact setting.
		info, statErr := os.Stat(configuredRoot)
		require.NoError(t, statErr)
		require.True(t, info.IsDir())

		artifactDirectory, err = os.MkdirTemp(configuredRoot, name+"-")
		require.NoError(t, err)
	}

	artifactPath := filepath.Join(artifactDirectory, name+".png")
	//nolint:gosec // The path sits under the operator's own artifact root.
	require.NoError(t, os.WriteFile(artifactPath, screenshot, 0o600))
	t.Logf(
		"browser screenshot path=%s dimensions=%dx%d",
		artifactPath,
		configuration.Width,
		configuration.Height,
	)
}
