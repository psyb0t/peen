package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newReconstructionMessage builds one durable transcript row for plan tests.
// Sequence mirrors the row's position in the fixture so FromSequence and
// ToSequence assertions can read directly off the call site.
func newReconstructionMessage(
	role models.MessageRole,
	sequence int64,
	turnID uuid.UUID,
) *models.Message {
	return &models.Message{
		ID:       uuid.New(),
		Role:     role,
		Sequence: sequence,
		TurnID:   turnID,
	}
}

func TestBuildReconstructionPlan(t *testing.T) {
	t.Parallel()

	t.Run("alternating user and assistant messages", func(t *testing.T) {
		t.Parallel()

		turn := uuid.New()
		messages := []*models.Message{
			newReconstructionMessage(models.MessageRoleUser, 0, turn),
			newReconstructionMessage(models.MessageRoleAssistant, 1, turn),
			newReconstructionMessage(models.MessageRoleUser, 2, turn),
			newReconstructionMessage(models.MessageRoleAssistant, 3, turn),
		}

		plan, err := buildReconstructionPlan(1, false, messages)
		require.NoError(t, err)
		require.Len(t, plan.Units, len(messages))

		for i, unit := range plan.Units {
			assert.Equal(t, 1, unit.MessageCount)
			assert.Equal(t, messages[i].ID, unit.FromMessageID)
			assert.Equal(t, messages[i].ID, unit.ToMessageID)
		}
	})

	t.Run("tool results join the assistant call into one unit", func(t *testing.T) {
		t.Parallel()

		turn := uuid.New()
		assistant := newReconstructionMessage(
			models.MessageRoleAssistant,
			1,
			turn,
		)
		toolOne := newReconstructionMessage(models.MessageRoleTool, 2, turn)
		toolTwo := newReconstructionMessage(models.MessageRoleTool, 3, turn)
		messages := []*models.Message{
			newReconstructionMessage(models.MessageRoleUser, 0, turn),
			assistant,
			toolOne,
			toolTwo,
		}

		plan, err := buildReconstructionPlan(0, false, messages)
		require.NoError(t, err)
		require.Len(t, plan.Units, 2)

		unit := plan.Units[1]
		assert.Equal(t, 3, unit.MessageCount)
		assert.Equal(t, assistant.ID, unit.FromMessageID)
		assert.Equal(t, toolTwo.ID, unit.ToMessageID)
		assert.Equal(t, assistant.Sequence, unit.FromSequence)
		assert.Equal(t, toolTwo.Sequence, unit.ToSequence)
	})

	t.Run("a non-tool row after a tool run starts a fresh unit", func(t *testing.T) {
		t.Parallel()

		turn := uuid.New()
		nextUser := newReconstructionMessage(models.MessageRoleUser, 4, turn)
		messages := []*models.Message{
			newReconstructionMessage(models.MessageRoleUser, 0, turn),
			newReconstructionMessage(models.MessageRoleAssistant, 1, turn),
			newReconstructionMessage(models.MessageRoleTool, 2, turn),
			newReconstructionMessage(models.MessageRoleTool, 3, turn),
			nextUser,
		}

		plan, err := buildReconstructionPlan(0, false, messages)
		require.NoError(t, err)
		require.Len(t, plan.Units, 3)

		fresh := plan.Units[2]
		assert.Equal(t, 1, fresh.MessageCount)
		assert.Equal(t, nextUser.ID, fresh.FromMessageID)
		assert.Equal(t, nextUser.ID, fresh.ToMessageID)
	})

	t.Run("a leading tool row with no preceding unit stands alone", func(t *testing.T) {
		t.Parallel()

		orphan := newReconstructionMessage(
			models.MessageRoleTool,
			0,
			uuid.New(),
		)

		var (
			plan reconstructionPlan
			err  error
		)

		require.NotPanics(t, func() {
			plan, err = buildReconstructionPlan(
				0,
				false,
				[]*models.Message{orphan},
			)
		})
		require.NoError(t, err)
		require.Len(t, plan.Units, 1)

		unit := plan.Units[0]
		assert.Equal(t, 1, unit.MessageCount)
		assert.Equal(t, orphan.ID, unit.FromMessageID)
		assert.Equal(t, orphan.ID, unit.ToMessageID)
	})

	t.Run("a nil entry reports a plan mismatch", func(t *testing.T) {
		t.Parallel()

		_, err := buildReconstructionPlan(0, false, []*models.Message{nil})
		require.ErrorIs(t, err, ErrCompactionPlanMismatch)
	})

	t.Run("an unknown role reports invalid state", func(t *testing.T) {
		t.Parallel()

		messages := []*models.Message{
			newReconstructionMessage(
				models.MessageRole("bogus"),
				0,
				uuid.New(),
			),
		}

		_, err := buildReconstructionPlan(0, false, messages)
		require.ErrorIs(t, err, commerr.ErrInvalidState)
	})

	t.Run("empty history keeps the caller's lead state", func(t *testing.T) {
		t.Parallel()

		const leadCount = 3

		plan, err := buildReconstructionPlan(leadCount, true, nil)
		require.NoError(t, err)
		assert.Empty(t, plan.Units)
		assert.Equal(t, leadCount, plan.LeadCount)
		assert.True(t, plan.HasSummary)
	})
}

func TestReconstructionPlanVerify(t *testing.T) {
	t.Parallel()

	turn := uuid.New()
	history := []*models.Message{
		newReconstructionMessage(models.MessageRoleUser, 0, turn),
		newReconstructionMessage(models.MessageRoleAssistant, 1, turn),
		newReconstructionMessage(models.MessageRoleTool, 2, turn),
	}

	plan, err := buildReconstructionPlan(1, false, history)
	require.NoError(t, err)

	assembled := []elelem.Message{
		{Role: elelem.RoleSystem},
		{Role: elelem.RoleUser},
		{Role: elelem.RoleAssistant},
		{Role: elelem.RoleTool},
	}

	mismatchedRole := []elelem.Message{
		{Role: elelem.RoleSystem},
		{Role: elelem.RoleUser},
		{Role: elelem.RoleUser},
		{Role: elelem.RoleTool},
	}

	extraTail := append([]elelem.Message{}, assembled...)
	extraTail = append(
		extraTail,
		elelem.Message{Role: elelem.RoleUser},
		elelem.Message{Role: elelem.RoleTool},
	)

	testCases := []struct {
		name     string
		plan     reconstructionPlan
		messages []elelem.Message
		wantErr  error
	}{
		{
			name:     "matches the assembled transcript",
			plan:     plan,
			messages: assembled,
		},
		{
			name:     "extra messages after the planned units are ignored",
			plan:     plan,
			messages: extraTail,
		},
		{
			name:     "a transcript shorter than the plan needs",
			plan:     plan,
			messages: assembled[:len(assembled)-1],
			wantErr:  ErrCompactionPlanMismatch,
		},
		{
			name:     "a role differs at a planned offset",
			plan:     plan,
			messages: mismatchedRole,
			wantErr:  ErrCompactionPlanMismatch,
		},
		{
			name: "a summary with no lead room is invalid",
			plan: reconstructionPlan{
				LeadCount:  0,
				HasSummary: true,
			},
			messages: assembled,
			wantErr:  ErrCompactionPlanMismatch,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.plan.verify(tc.messages)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestReconstructionPlanCoverableUnits(t *testing.T) {
	t.Parallel()

	turnOne := uuid.New()
	turnTwo := uuid.New()
	turnThree := uuid.New()

	testCases := []struct {
		name  string
		units []reconstructionUnit
		want  int
	}{
		{
			name: "the newest turn's units are never coverable",
			units: []reconstructionUnit{
				{TurnID: turnOne},
				{TurnID: turnOne},
				{TurnID: turnTwo},
				{TurnID: turnTwo},
				{TurnID: turnThree},
				{TurnID: turnThree},
			},
			want: 4,
		},
		{
			name: "a single shared turn covers nothing",
			units: []reconstructionUnit{
				{TurnID: turnOne},
				{TurnID: turnOne},
				{TurnID: turnOne},
			},
		},
		{
			name: "no units cover nothing",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			plan := reconstructionPlan{Units: tc.units}
			assert.Equal(t, tc.want, plan.coverableUnits())
		})
	}
}

func TestReconstructionPlanSummaryStart(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		leadCount  int
		hasSummary bool
		want       int
	}{
		{
			name:      "no summary uses the lead count directly",
			leadCount: 2,
			want:      2,
		},
		{
			name:       "a summary sits one message before the lead count",
			leadCount:  2,
			hasSummary: true,
			want:       1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			plan := reconstructionPlan{
				LeadCount:  tc.leadCount,
				HasSummary: tc.hasSummary,
			}
			assert.Equal(t, tc.want, plan.summaryStart())
		})
	}
}

func TestReconstructionPlanMessageCount(t *testing.T) {
	t.Parallel()

	plan := reconstructionPlan{
		Units: []reconstructionUnit{
			{MessageCount: 1},
			{MessageCount: 3},
			{MessageCount: 1},
		},
	}

	testCases := []struct {
		name  string
		units int
		want  int
	}{
		{name: "zero units", units: 0, want: 0},
		{name: "one plain unit", units: 1, want: 1},
		{
			name:  "including the multi-message tool unit",
			units: 2,
			want:  4,
		},
		{name: "every unit", units: 3, want: 5},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, plan.messageCount(tc.units))
		})
	}
}

// TestReconstructionPlanAdvance asserts the slot invariant a second
// compaction depends on: summaryStart must read the same after advance as it
// did before, so a later compaction replaces the existing summary message
// rather than inserting a second one next to it.
func TestReconstructionPlanAdvance(t *testing.T) {
	t.Parallel()

	turn := uuid.New()
	plan := reconstructionPlan{
		LeadCount: 2,
		Units: []reconstructionUnit{
			{MessageCount: 1, TurnID: turn},
			{MessageCount: 3, TurnID: turn},
			{MessageCount: 1, TurnID: turn},
		},
	}

	before := plan.summaryStart()

	plan.advance(1)

	assert.True(t, plan.HasSummary)
	assert.Len(t, plan.Units, 2)
	assert.Equal(
		t,
		before,
		plan.summaryStart(),
		"a second compaction must replace the same summary slot",
	)

	firstLeadCount := plan.LeadCount
	firstSummaryStart := plan.summaryStart()

	// A second compaction in the same run must land on the same slot again,
	// not walk LeadCount forward a second time.
	plan.advance(1)

	assert.Equal(t, firstLeadCount, plan.LeadCount)
	assert.Equal(t, firstSummaryStart, plan.summaryStart())
	assert.Len(t, plan.Units, 1)
}

func TestPlanLeadCount(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		systemPrompt string
		history      *session.History
		want         int
	}{
		{
			name:    "no system prompt and no compaction",
			history: &session.History{},
		},
		{
			name:         "a system prompt with no compaction",
			systemPrompt: "sys",
			history:      &session.History{},
			want:         1,
		},
		{
			name:         "a system prompt and an active compaction",
			systemPrompt: "sys",
			history:      &session.History{Compaction: &models.Compaction{}},
			want:         2,
		},
		{
			name:    "no system prompt but an active compaction",
			history: &session.History{Compaction: &models.Compaction{}},
			want:    1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := planLeadCount(tc.systemPrompt, tc.history)
			assert.Equal(t, tc.want, got)
		})
	}
}
