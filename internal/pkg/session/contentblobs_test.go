package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/contentblob"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// largeTestDocument repeats one long string the way a turn context repeats its
// system prompt and history, so compaction has something to remove.
func largeTestDocument(t *testing.T, label string) json.RawMessage {
	t.Helper()

	text := strings.Repeat(label+" saved step text with <markup> & \"quotes\"\n",
		200)

	encoded, err := json.Marshal(map[string]any{
		"systemPrompt": text,
		"messages":     []map[string]string{{"content": text}, {"content": text}},
	})
	require.NoError(t, err)

	return encoded
}

func storedColumn(t *testing.T, handle *db.Handle, query string,
	id uuid.UUID,
) string {
	t.Helper()

	var value string
	require.NoError(t, handle.SQLDB().QueryRowContext(t.Context(), query,
		id.String()).Scan(&value))

	return value
}

func TestActivityJSONIsStoredCompactedAndReadBackExactly(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)

	input := activityTestInput(models.ActivityKindModel)
	input.Input = largeTestDocument(t, "input")

	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), input)
	require.NoError(t, err)
	assert.Equal(t, string(input.Input), claim.Activity.InputJSON,
		"the returned activity holds the original input")

	result := CompleteActivityInput{
		Output:  largeTestDocument(t, "output"),
		Context: largeTestDocument(t, "context"),
	}

	completed, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, result)
	require.NoError(t, err)
	assert.Equal(t, string(result.Output), *completed.OutputJSON)

	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, result)
	require.NoError(t, err, "repeating the same result compares the "+
		"restored columns with the new ones")

	for _, column := range []string{
		"input_json", "output_json",
		"context_json",
	} {
		stored := storedColumn(t, fixture.handle, "SELECT "+column+
			" FROM execution_activities WHERE id = ?", claim.Activity.ID)
		assert.True(t, contentblob.IsCompacted([]byte(stored)), column)
		assert.Less(t, len(stored), len(input.Input)/4, column)
	}

	read, err := fixture.store.GetExecutionActivity(t.Context(),
		fixture.sessionID, claim.Activity.ID)
	require.NoError(t, err)
	assert.Equal(t, string(input.Input), read.InputJSON)
	assert.Equal(t, string(result.Output), *read.OutputJSON)
	assert.Equal(t, string(result.Context), read.ContextJSON)

	reused, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), input)
	require.NoError(t, err)
	assert.Equal(t, ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, string(result.Output), *reused.Activity.OutputJSON)
}

func TestExecutionContextIsStoredCompactedAndReadBackExactly(t *testing.T) {
	t.Parallel()
	store, handle := openTestStore(t)
	session := newTestSession(t.Context(), t, store)
	lease := startAgentRunTestTurn(t.Context(), t, store, session.ID)

	input := executionTestInput(lease.TurnID)
	input.Context = largeTestDocument(t, "execution")

	created, err := store.CreateExecution(t.Context(), session.ID, input)
	require.NoError(t, err)
	assert.Equal(t, string(input.Context), created.ContextJSON)

	stored := storedColumn(t, handle,
		"SELECT context_json FROM executions WHERE id = ?", created.ID)
	assert.True(t, contentblob.IsCompacted([]byte(stored)))

	read, err := store.GetExecution(t.Context(), session.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, string(input.Context), read.ContextJSON)

	again, err := store.CreateExecution(t.Context(), session.ID, input)
	require.NoError(t, err, "recreating the same execution compares the "+
		"restored context")
	assert.Equal(t, created.ID, again.ID)
}

func TestContentBlobStoreAcceptsOnlyMatchingBlobs(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)

	compacted, blobs := contentblob.Compact(largeTestDocument(t, "blob"))
	require.NotEmpty(t, blobs)
	require.True(t, contentblob.IsCompacted(compacted))

	require.NoError(t, store.PutContentBlobs(t.Context(), blobs))
	require.NoError(t, store.PutContentBlobs(t.Context(), blobs),
		"storing a blob twice is a no-op")

	found, err := store.GetContentBlobs(t.Context(),
		[]string{blobs[0].Hash, strings.Repeat("0", 64)})
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{blobs[0].Hash: blobs[0].Data}, found)

	forged := []contentblob.Blob{{Hash: blobs[0].Hash, Data: []byte(`"x"`)}}
	require.ErrorIs(t, store.PutContentBlobs(t.Context(), forged),
		commerr.ErrValidationFailed)

	tooMany := make([]string, 4097)
	_, err = store.GetContentBlobs(t.Context(), tooMany)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}
