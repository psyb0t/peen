package cronconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testOptions() Options {
	return Options{
		MaxMessageBytes: 128,
		MaxSchemaBytes:  2048,
		ModelIDs:        []string{"provider/model"},
	}
}

func cronYAML(schedule string, extra string) string {
	return fmt.Sprintf(`jobs:
  - name: report
    schedule: %q
    workspace: /srv/work
    message: run report
%s`, schedule, extra)
}

func TestParseAcceptsFiveSixFieldsAndDescriptors(t *testing.T) {
	t.Parallel()

	testCases := []string{
		"0 9 * * *",
		"0 0 9 * * *",
		"@daily",
	}
	for _, scheduleText := range testCases {
		t.Run(scheduleText, func(t *testing.T) {
			t.Parallel()

			jobs, err := Parse([]byte(cronYAML(scheduleText, "")), testOptions())
			require.NoError(t, err)
			require.Len(t, jobs, 1)
			assert.Equal(t, scheduleText, jobs[0].ScheduleText)
			assert.Equal(t, "UTC", jobs[0].TimeZone)
			assert.Equal(t, SessionContinue, jobs[0].Session)
			assert.NotNil(t, jobs[0].Schedule)
		})
	}
}

func TestParseUsesNamedTimeZoneAcrossDaylightSaving(t *testing.T) {
	t.Parallel()

	jobs, err := Parse([]byte(cronYAML("0 0 * * *",
		"    timeZone: America/New_York\n")), testOptions())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	from := time.Date(2026, time.March, 8, 6, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.March, 9, 4, 0, 0, 0, time.UTC)
	assert.Equal(t, want, jobs[0].Schedule.Next(from))
}

func TestParseRejectsEmbeddedTimeZonePrefixes(t *testing.T) {
	t.Parallel()

	for _, scheduleText := range []string{
		"CRON_TZ=America/New_York 0 9 * * *",
		"  TZ=UTC 0 9 * * *  ",
	} {
		t.Run(strings.TrimSpace(scheduleText), func(t *testing.T) {
			t.Parallel()

			_, err := Parse([]byte(cronYAML(scheduleText, "")), testOptions())
			require.ErrorContains(t, err, `job "report" field schedule`)
		})
	}
}

func TestParseRejectsUnsafeTimeZoneNames(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		timeZone string
	}{
		{name: "newline", timeZone: "UTC\nCRON_TZ=Etc/GMT"},
		{name: "nul", timeZone: "UTC\x00"},
		{name: "local", timeZone: "Local"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			data := cronYAML(
				"@daily",
				fmt.Sprintf("    timeZone: %q\n", testCase.timeZone),
			)
			_, err := Parse([]byte(data), testOptions())
			require.ErrorContains(t, err, `job "report" field timeZone`)
		})
	}
}

func TestParseAcceptsValidatedJobOptions(t *testing.T) {
	t.Parallel()

	data := []byte(cronYAML("@daily", `    timeZone: Europe/Paris
    agentSpace: docker:default
    session: new
    model: provider/model
    reasoningEffort: high
    timeout: 30m
    outputSchema:
      type: object
      properties:
        ok:
          type: boolean
    telegramChatId: -100123
`))
	options := testOptions()
	options.TelegramEnabled = true
	jobs, err := Parse(data, options)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	job := jobs[0]
	assert.Equal(t, "Europe/Paris", job.TimeZone)
	assert.Equal(t, "docker:default", job.AgentSpace)
	assert.Equal(t, SessionNew, job.Session)
	assert.Equal(t, "provider/model", job.Model)
	assert.Equal(t, "high", job.ReasoningEffort)
	assert.Equal(t, 30*time.Minute, job.Timeout)
	assert.JSONEq(t, `{"type":"object","properties":{"ok":{"type":"boolean"}}}`,
		string(job.OutputSchema))
	assert.Equal(t, int64(-100123), job.TelegramChatID)
}

func TestParsePreservesEmptyOutputSchema(t *testing.T) {
	t.Parallel()

	jobs, err := Parse([]byte(cronYAML("@daily", "    outputSchema: {}\n")),
		testOptions())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.JSONEq(t, `{}`, string(jobs[0].OutputSchema))
}

func TestParseReturnsEmptyForMissingAndEmptyConfiguration(t *testing.T) {
	t.Parallel()

	jobs, err := Parse(nil, testOptions())
	require.NoError(t, err)
	assert.Empty(t, jobs)

	configDirectory := t.TempDir()
	jobs, err = Load(configDirectory, testOptions())
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestParseClassifiesMalformedYAML(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte("jobs: [\n"), testOptions())
	require.ErrorIs(t, err, commerr.ErrParseFailed)
	assert.NotContains(t, err.Error(), "jobs: [")
}

func TestLoadReadsCronConfigurationFromAgentsDirectory(t *testing.T) {
	t.Parallel()

	configDirectory := t.TempDir()
	agentsDirectory := filepath.Join(configDirectory, ".agents")
	require.NoError(t, os.MkdirAll(agentsDirectory, 0o750))
	filename := filepath.Join(agentsDirectory, cronConfigFileName)
	require.NoError(t, os.WriteFile(filename,
		[]byte(cronYAML("@daily", "")), 0o600))

	jobs, err := Load(configDirectory, testOptions())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "report", jobs[0].Name)
}

func TestLoadRefusesNonRegularConfigurationFile(t *testing.T) {
	t.Parallel()

	configDirectory := t.TempDir()
	agentsDirectory := filepath.Join(configDirectory, ".agents")
	require.NoError(t, os.MkdirAll(agentsDirectory, 0o750))
	filename := filepath.Join(agentsDirectory, cronConfigFileName)
	require.NoError(t, syscall.Mkfifo(filename, 0o600))

	_, err := Load(configDirectory, testOptions())
	require.ErrorContains(t, err, "must be a regular file")
}

func TestParseRejectsMalformedAndUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	unknownField := cronYAML("@daily", "    password: never-echo-this\n")
	duplicateKey := "jobs:\n  - name: report\n    name: other\n" +
		"    schedule: '@daily'\n    workspace: /srv/work\n    message: run\n"
	duplicateJob := `jobs:
  - name: report
    schedule: '@daily'
    workspace: /srv/work
    message: first
  - name: report
    schedule: '@daily'
    workspace: /srv/work
    message: second
`
	secondDocument := cronYAML("@daily", "") + "---\njobs: []\n"
	oversized := strings.Repeat("x", maximumConfigBytes+1)

	testCases := []struct {
		name string
		data string
	}{
		{name: "unknown field", data: unknownField},
		{name: "duplicate key", data: duplicateKey},
		{name: "duplicate job", data: duplicateJob},
		{name: "second document", data: secondDocument},
		{name: "oversized file", data: oversized},
		{name: "relative workspace", data: strings.Replace(
			cronYAML("@daily", ""),
			"workspace: /srv/work", "workspace: relative", 1,
		)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse([]byte(testCase.data), testOptions())
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "never-echo-this")
		})
	}
}

func TestParseEnforcesYAMLAliasLimit(t *testing.T) {
	t.Parallel()

	var yaml strings.Builder
	yaml.WriteString("jobs: &jobs []\nextra: [")
	for range maximumYAMLAliases + 1 {
		yaml.WriteString("*jobs,")
	}
	yaml.WriteString("]\n")
	_, err := Parse([]byte(yaml.String()), testOptions())
	require.ErrorContains(t, err, "too many aliases")
}

func TestParseRejectsInvalidJobFields(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		extra  string
		option func(*Options)
		field  string
	}{
		{name: "unknown model", extra: "    model: missing/model\n", field: "model"},
		{name: "invalid effort", extra: "    reasoningEffort: unlimited\n", field: "reasoningEffort"},
		{name: "negative timeout", extra: "    timeout: -1s\n", field: "timeout"},
		{name: "fractional timeout", extra: "    timeout: 500ms\n", field: "timeout"},
		{name: "excessive timeout", extra: "    timeout: 24h1s\n", field: "timeout"},
		{name: "invalid timezone", extra: "    timeZone: Mars/Olympus\n", field: "timeZone"},
		{name: "telegram disabled", extra: "    telegramChatId: 123\n", field: "telegramChatId"},
		{name: "message too long", extra: "", option: func(options *Options) {
			options.MaxMessageBytes = 2
		}, field: "message"},
		{name: "invalid schema", extra: "    outputSchema:\n      $ref: https://example.invalid/schema\n", field: "outputSchema"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			options := testOptions()
			if testCase.option != nil {
				testCase.option(&options)
			}
			_, err := Parse([]byte(cronYAML("@daily", testCase.extra)), options)
			require.ErrorContains(t, err, `job "report" field `+testCase.field)
		})
	}
}

func TestParseRejectsInvalidScheduleAndTimeoutDescriptor(t *testing.T) {
	t.Parallel()

	for _, scheduleText := range []string{
		"0 0 31 2 *",
		"@every -1s",
		"@every 500ms",
	} {
		t.Run(scheduleText, func(t *testing.T) {
			t.Parallel()

			_, err := Parse([]byte(cronYAML(scheduleText, "")), testOptions())
			require.ErrorContains(t, err, `job "report" field schedule`)
		})
	}
}
