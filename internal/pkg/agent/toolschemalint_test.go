package agent

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolSchemaLintAllowedCapitals are words written in capitals because that
// is their name, not for emphasis.
//
//nolint:gochecknoglobals // Read-only test table.
var toolSchemaLintAllowedCapitals = map[string]bool{
	"HTML": true, "JSON": true, "SIGTERM": true, "SIGKILL": true,
	"SKILL": true, "AGENTS": true,
}

var capitalWord = regexp.MustCompile(`\b[A-Z]{4,}\b`)

// undescribedProperties walks a JSON schema and returns the dotted path of
// every property that has no description.
func undescribedProperties(prefix string, schema map[string]any) []string {
	var missing []string

	properties, _ := schema["properties"].(map[string]any)
	for name, raw := range properties {
		property, _ := raw.(map[string]any)
		path := prefix + name

		description, _ := property["description"].(string)
		if strings.TrimSpace(description) == "" {
			missing = append(missing, path)
		}

		missing = append(missing, undescribedProperties(path+".", property)...)

		if items, ok := property["items"].(map[string]any); ok {
			missing = append(missing,
				undescribedProperties(path+"[].", items)...)
		}
	}

	return missing
}

// Every tool description and parameter is written the way the model reads
// it best: each property described, one line per paragraph, and capitals
// only for names.
func TestToolSchemasAreWrittenForTheModel(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		description string
		schema      string
	}{
		{toolNameListFiles, listFilesDescription, listFilesSchema},
		{toolNameSearchText, searchTextDescription, searchTextSchema},
		{toolNameReadFile, readFileDescription, readFileSchema},
		{toolNameWebFetch, webFetchDescription, webFetchSchema},
		{toolNameWriteFile, writeFileDescription, writeFileSchema},
		{toolNameEditFile, editFileDescription, editFileSchema},
		{toolNameApplyPatch, applyPatchDescription, applyPatchSchema},
		{toolNameMovePath, movePathDescription, movePathSchema},
		{toolNameMakeDirectory, makeDirectoryDescription, makeDirectorySchema},
		{toolNameRemovePath, removePathDescription, removePathSchema},
		{toolNameRunCommand, runCommandDescription, runCommandSchema},
		{toolNameListJobs, listJobsDescription, listJobsSchema},
		{toolNameReadJobOutput, readJobOutputDescription, readJobOutputSchema},
		{toolNameWaitJob, waitJobDescription, waitJobSchema},
		{toolNameSignalJob, signalJobDescription, signalJobSchema},
		{toolNameUseSkill, useSkillDescription, useSkillSchema},
		{
			toolNameChangeDirectory, changeDirectoryDescription,
			changeDirectorySchema,
		},
		{toolNameLaunchAgent, launchAgentDescription, launchAgentSchema},
		{toolNameSendMessage, sendMessageDescription, sendMessageSchema},
		{toolNameWaitMessages, waitMessagesDescription, waitMessagesSchema},
		{toolNameStopAgent, stopAgentDescription, stopAgentSchema},
		{toolNameAsk, askDescription, askSchema},
		{
			toolNameSearchExecutions, searchExecutionsDescription,
			searchExecutionsSchema,
		},
		{
			toolNameReadExecution, readExecutionDescription,
			readExecutionSchema,
		},
		{toolNameReadMetrics, readMetricsDescription, readMetricsSchema},
		{
			toolNameResumeExecution, resumeExecutionDescription,
			resumeExecutionSchema,
		},
		{
			toolNameResolveExecutionActivity,
			resolveExecutionActivityDescription,
			resolveExecutionActivitySchema,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var schema map[string]any
			require.NoError(t, json.Unmarshal([]byte(tc.schema), &schema))
			assert.Empty(t, undescribedProperties("", schema),
				"every parameter needs a description")

			for paragraph := range strings.SplitSeq(tc.description, "\n\n") {
				assert.NotContains(t, paragraph, "\n",
					"a paragraph must stay on one line")
			}

			for _, word := range capitalWord.FindAllString(tc.description, -1) {
				assert.True(t, toolSchemaLintAllowedCapitals[word],
					"%q in the description reads as emphasis", word)
			}
		})
	}
}
