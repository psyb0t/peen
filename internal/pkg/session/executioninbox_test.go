package session

import (
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func inboxTestLease(t *testing.T, fixture executionFixture) Lease {
	t.Helper()
	lease, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID,
		RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()})
	require.NoError(t, err)

	return lease
}

func inboxTestInput() AcceptExecutionMessageInput {
	return AcceptExecutionMessageInput{
		RequestID: uuid.New(), SourceEventID: uuid.New(),
		WorkingDirectory: testWorkspace, Message: "continue example work",
		Parts: []ExecutionInboxPart{{
			SkillName: "example", Content: "frozen skill content",
		}},
		Capacity: 32,
	}
}

func TestAcceptExecutionMessageRetainsDistinctIdenticalText(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	input := inboxTestInput()
	first, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	require.Len(t, first.Events, 2)
	assert.Equal(t, EventTypeUserMessageCreated, first.Events[0].EventType)
	assert.Equal(t, EventTypeUserMessageQueued, first.Events[1].EventType)
	assert.Equal(t, input.RequestID, first.Events[0].RequestID)
	require.NotNil(t, first.Message.SourceEventID)
	assert.Equal(t, input.SourceEventID, *first.Message.SourceEventID)
	assert.Equal(t, models.InboxMessageStateQueued, first.Message.State)
	assert.Contains(t, first.Message.PartsJSON, input.Parts[0].Content)
	repeated, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	assert.Equal(t, first.Message.ID, repeated.Message.ID)
	assert.Empty(t, repeated.Events)
	input.RequestID = uuid.New()
	second, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	assert.NotEqual(t, first.Message.ID, second.Message.ID)
	assert.Equal(t, first.Message.Sequence+1, second.Message.Sequence)
	events, err := fixture.store.ListEvents(t.Context(), fixture.sessionID,
		ListEventsOptions{EventTypes: []string{
			EventTypeUserMessageCreated, EventTypeUserMessageQueued,
		}})
	require.NoError(t, err)
	assert.Len(t, events.Items, 4)
}

func TestAcceptExecutionMessageRefusesChangedRequest(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*AcceptExecutionMessageInput)
	}{
		{name: "text", change: func(input *AcceptExecutionMessageInput) {
			input.Message = "changed"
		}},
		{name: "directory", change: func(input *AcceptExecutionMessageInput) {
			input.WorkingDirectory = "/different"
		}},
		{name: "source", change: func(input *AcceptExecutionMessageInput) {
			input.SourceEventID = uuid.New()
		}},
		{name: "skill", change: func(input *AcceptExecutionMessageInput) {
			input.Parts[0].Content = "changed"
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			lease := inboxTestLease(t, fixture)
			input := inboxTestInput()
			_, err := fixture.store.AcceptExecutionMessage(t.Context(),
				fixture.root.ID, lease, input)
			require.NoError(t, err)
			tc.change(&input)
			_, err = fixture.store.AcceptExecutionMessage(t.Context(),
				fixture.root.ID, lease, input)
			require.ErrorIs(t, err, commerr.ErrConflict)
		})
	}
}

func TestAcceptExecutionMessageValidation(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*AcceptExecutionMessageInput)
	}{
		{name: "identity", change: func(input *AcceptExecutionMessageInput) {
			input.RequestID = uuid.Nil
		}},
		{name: "capacity", change: func(input *AcceptExecutionMessageInput) {
			input.Capacity = 0
		}},
		{name: "directory", change: func(input *AcceptExecutionMessageInput) {
			input.WorkingDirectory = " "
		}},
		{name: "message", change: func(input *AcceptExecutionMessageInput) {
			input.Message = "\t"
		}},
		{name: "skill name", change: func(input *AcceptExecutionMessageInput) {
			input.Parts[0].SkillName = " "
		}},
		{name: "skill content", change: func(input *AcceptExecutionMessageInput) {
			input.Parts[0].Content = ""
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			lease := inboxTestLease(t, fixture)
			input := inboxTestInput()
			tc.change(&input)
			_, err := fixture.store.AcceptExecutionMessage(t.Context(),
				fixture.root.ID, lease, input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestAcceptExecutionMessageCapacityCountsSkills(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	input := inboxTestInput()
	input.Capacity = 2
	_, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	input.RequestID = uuid.New()
	input.Parts = nil
	_, err = fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestAcceptExecutionMessageStorageLimits(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		skills bool
	}{{name: "message"}, {name: "skills", skills: true}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			lease := inboxTestLease(t, fixture)
			input := inboxTestInput()
			oversized := strings.Repeat("x", fixture.store.maxStoredMessageBytes+1)
			if tc.skills {
				input.Parts[0].Content = oversized
			} else {
				input.Message = oversized
			}
			_, err := fixture.store.AcceptExecutionMessage(t.Context(),
				fixture.root.ID, lease, input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestAcceptExecutionMessageConcurrentDuplicate(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	input := inboxTestInput()
	var group sync.WaitGroup

	for range 8 {
		group.Go(func() {
			_, err := fixture.store.AcceptExecutionMessage(t.Context(),
				fixture.root.ID, lease, input)
			assert.NoError(t, err)
		})
	}

	group.Wait()
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}

func TestAcceptExecutionMessagePauseKeepsExistingRequest(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	input := inboxTestInput()
	accepted, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	repeated, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	assert.Equal(t, accepted.Message.ID, repeated.Message.ID)
	assert.Empty(t, repeated.Events)
	input.RequestID = uuid.New()
	_, err = fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
}

func TestAcceptExecutionMessageRejectsReplacedHolder(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	_, err := fixture.store.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		fixture.root.ID, RestoreExecutionTurnInput{
			OwnerGenerationID: uuid.New(), PreviousLeaseID: lease.ID,
		})
	require.NoError(t, err)
	_, err = fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, inboxTestInput())
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}
