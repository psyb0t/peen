package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	compactionTranscriptOpenTag  = "<prior-conversation>"
	compactionTranscriptCloseTag = "</prior-conversation>"
	compactionRoleSeparator      = ": "
	compactionToolCallLead       = "tool call "
	compactionArgumentSeparator  = " "
	compactionLineBreak          = "\n"

	// compactionReasonBudgetExceeded is the stable reason logged when a
	// request's transcript no longer fits the configured budget.
	compactionReasonBudgetExceeded = "context_budget_exceeded"
	compactionOutcomeSucceeded     = "succeeded"
	compactionOutcomeFailed        = "failed"
)

// compactionSummary is one summarization call's result.
type compactionSummary struct {
	Text         string
	ModelID      string
	InputTokens  int64
	OutputTokens int64
}

// summarizeFunc produces one replacement summary from rendered transcript
// text. Production calls the configured compaction model; a test substitutes a
// deterministic seam so prefix selection can be proven without a provider.
type summarizeFunc func(context.Context, string) (compactionSummary, error)

// compactionHookPayload gives hooks the stable facts of one real summary
// replacement. It deliberately excludes transcript content and summary text.
type compactionHookPayload struct {
	Workspace       string `json:"workspace"`
	EstimatedTokens int    `json:"estimatedTokens"`
	BudgetTokens    int    `json:"budgetTokens"`
	Round           int    `json:"round"`
	UnitsCovered    int    `json:"unitsCovered"`
	MessagesCovered int    `json:"messagesCovered"`
	SummaryTokens   int64  `json:"summaryTokens,omitempty"`
}

type compactionHook func(
	context.Context,
	harness.HookEvent,
	compactionHookPayload,
) error

// compactionOptions are the deployment settings one compactor needs.
type compactionOptions struct {
	Store           *session.Store
	Models          ModelResolver
	Metrics         *metrics.Metrics
	ModelReference  string
	Prompt          string
	PromptHash      string
	MaxOutputTokens int
	Timeout         time.Duration
	Workspace       string
	Hook            compactionHook

	// Summarize replaces the provider call. Nil uses the configured model.
	Summarize summarizeFunc
}

// withDefaults fills the summary-only settings a caller left at zero, so an
// embedding Go program does not have to restate every deployment bound.
func (o compactionOptions) withDefaults(defaultModel string) compactionOptions {
	if o.ModelReference == "" {
		o.ModelReference = defaultModel
	}

	if o.MaxOutputTokens <= 0 {
		o.MaxOutputTokens = defaultCompactionOutputTokens
	}

	if o.Timeout <= 0 {
		o.Timeout = defaultCompactionTimeout
	}

	return o
}

func (o compactionOptions) validate() error {
	if o.Store == nil || o.Models == nil {
		return ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"compaction dependency",
		)
	}

	if o.ModelReference == "" || o.Prompt == "" ||
		o.MaxOutputTokens <= 0 || o.Timeout <= 0 {
		return ctxerrors.Wrap(
			ErrInvalidCompactionOptions,
			"compaction settings",
		)
	}

	return nil
}

// compactor replaces a completed history prefix with a durable summary when a
// request exceeds the context budget.
//
// One compactor serves one turn. It carries that turn's reconstruction plan
// and advances it after each committed compaction, so a second compaction in
// the same run plans against what is left instead of re-covering stored rows.
// Elelem calls the limit hook serially within a run, so the state needs no
// lock.
type compactor struct {
	options   compactionOptions
	sessionID uuid.UUID
	turnID    uuid.UUID
	plan      reconstructionPlan
	active    *models.Compaction
}

func newCompactor(
	options compactionOptions,
	sessionID uuid.UUID,
	turnID uuid.UUID,
	plan reconstructionPlan,
	active *models.Compaction,
) (*compactor, error) {
	if err := options.validate(); err != nil {
		return nil, ctxerrors.Wrap(err, "validate compaction options")
	}

	if sessionID == uuid.Nil || turnID == uuid.Nil {
		return nil, ctxerrors.Wrap(
			ErrInvalidCompactionOptions,
			"compaction session or turn",
		)
	}

	return &compactor{
		options:   options,
		sessionID: sessionID,
		turnID:    turnID,
		plan:      plan,
		active:    active,
	}, nil
}

// handle is Peen's PreMaxTokensReached hook.
//
// Every failure path returns an error and leaves event.Messages as it found
// them. Elelem aborts the run on a handler error, which is the point: the
// alternative to a stored summary is dropping conversation the caller can
// neither see in the response nor reconstruct afterwards.
//
//nolint:funlen,nonamedreturns // One compaction safety boundary.
func (c *compactor) handle(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
) (handleErr error) {
	startedAt := time.Now()

	ctxscope.GetLogger(ctx).Debug(
		"context compaction started",
		"estimated_tokens", event.EstimatedTokens,
		"budget_tokens", event.BudgetTokens,
		"round", event.Round,
	)
	defer func() {
		ctxscope.GetLogger(ctx).Debug(
			"context compaction finished",
			"outcome", compactionOutcome(handleErr),
			"estimated_tokens", event.EstimatedTokens,
			"budget_tokens", event.BudgetTokens,
			"round", event.Round,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
	}()

	if err := c.plan.verify(event.Messages); err != nil {
		return ctxerrors.Wrap(err, "verify reconstruction plan")
	}

	units, err := c.selectPrefix(event)
	if err != nil {
		return err
	}

	messagesCovered := c.plan.messageCount(units)

	payload := c.hookPayload(event, units, messagesCovered, 0)
	if err := c.runHook(ctx, harness.HookEventPreCompact, payload); err != nil {
		return ctxerrors.Wrap(err, "run pre-compaction hooks")
	}

	summary, err := c.summarize(ctx, c.renderTranscript(event.Messages, units))
	if err != nil {
		return ctxerrors.Wrap(err, "summarize completed prefix")
	}

	text := strings.TrimSpace(summary.Text)
	if text == "" {
		return ctxerrors.Wrap(
			ErrCompactionEmptySummary,
			"the compaction model returned no summary",
		)
	}

	if err := c.commit(ctx, event, units, text, summary); err != nil {
		return err
	}

	payload = c.hookPayload(
		event,
		units,
		messagesCovered,
		summary.OutputTokens,
	)
	if err := c.runHook(
		ctx,
		harness.HookEventPostCompact,
		payload,
	); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"post-compaction hook failed, continuing",
			"err", err,
			"units_covered", units,
			"messages_covered", messagesCovered,
		)
	}

	ctxscope.GetLogger(ctx).Info(
		"replaced completed history with a stored summary",
		"reason", compactionReasonBudgetExceeded,
		"session_id", c.sessionID,
		"units_covered", units,
		"messages_covered", messagesCovered,
		"summary_tokens", summary.OutputTokens,
		"estimated_tokens", event.EstimatedTokens,
		"budget_tokens", event.BudgetTokens,
		"round", event.Round,
	)

	return nil
}

func (c *compactor) hookPayload(
	event *elelem.TokenLimitEvent,
	units int,
	messages int,
	summaryTokens int64,
) compactionHookPayload {
	return compactionHookPayload{
		Workspace:       c.options.Workspace,
		EstimatedTokens: event.EstimatedTokens,
		BudgetTokens:    event.BudgetTokens,
		Round:           event.Round,
		UnitsCovered:    units,
		MessagesCovered: messages,
		SummaryTokens:   summaryTokens,
	}
}

func (c *compactor) runHook(
	ctx context.Context,
	event harness.HookEvent,
	payload compactionHookPayload,
) error {
	if c.options.Hook == nil {
		return nil
	}

	if err := c.options.Hook(ctx, event, payload); err != nil {
		return ctxerrors.Wrap(err, "run compaction hook")
	}

	return nil
}

func compactionOutcome(err error) string {
	if err == nil {
		return compactionOutcomeSucceeded
	}

	return compactionOutcomeFailed
}

// selectPrefix picks the smallest completed prefix whose removal leaves room
// for the leading context, the preserved raw tail, the current turn, and the
// configured summary allowance.
//
// Smallest, not largest: the covered prefix is also the summarization call's
// input, and that call deliberately has no budget hook of its own. Covering
// more than the budget requires grows that input for no gain.
func (c *compactor) selectPrefix(event *elelem.TokenLimitEvent) (int, error) {
	coverable := c.plan.coverableUnits()
	if coverable == 0 {
		return 0, ctxerrors.Wrap(
			ErrCompactionUnavailable,
			"no completed history is eligible for compaction",
		)
	}

	original := event.Messages
	defer func() { event.Messages = original }()

	for units := 1; units <= coverable; units++ {
		event.Messages = c.replacePrefix(
			original,
			units,
			compactionSummaryLead,
		)

		if _, err := event.IsOverBudget(); err != nil {
			return 0, ctxerrors.Wrap(err, "count compaction candidate")
		}

		if event.EstimatedTokens+c.options.MaxOutputTokens <=
			event.BudgetTokens {
			return units, nil
		}
	}

	return 0, ctxerrors.Wrapf(
		ErrCompactionUnavailable,
		"%d eligible units do not free the %d token summary allowance",
		coverable,
		c.options.MaxOutputTokens,
	)
}

// commit installs the real summary, recounts, and stores the row only when the
// authoritative count fits. A recount that still does not fit leaves the
// transcript untouched rather than persisting a replacement that bought
// nothing.
func (c *compactor) commit(
	ctx context.Context,
	event *elelem.TokenLimitEvent,
	units int,
	text string,
	summary compactionSummary,
) error {
	original := event.Messages
	event.Messages = c.replacePrefix(
		original,
		units,
		compactionSummaryLead+text,
	)

	over, err := event.IsOverBudget()
	if err != nil {
		event.Messages = original

		return ctxerrors.Wrap(err, "recount compacted transcript")
	}

	if over {
		event.Messages = original

		return ctxerrors.Wrapf(
			ErrCompactionInsufficient,
			"%d tokens against a %d budget after summarizing",
			event.EstimatedTokens,
			event.BudgetTokens,
		)
	}

	stored, err := c.persist(ctx, units, text, summary)
	if err != nil {
		event.Messages = original

		return err
	}

	c.active = stored
	c.plan.advance(units)

	return nil
}

// persist stores the immutable row describing exactly which durable messages
// the summary replaced.
func (c *compactor) persist(
	ctx context.Context,
	units int,
	text string,
	summary compactionSummary,
) (*models.Compaction, error) {
	covered := c.plan.Units[:units]
	first := covered[0]
	last := covered[len(covered)-1]

	input := session.CompactionInput{
		FromMessageID:      first.FromMessageID,
		ToMessageID:        last.ToMessageID,
		FromSequence:       first.FromSequence,
		ToSequence:         last.ToSequence,
		DirectFromSequence: first.FromSequence,
		DirectToSequence:   last.ToSequence,
		Summary:            text,
		SourceMessageCount: int64(c.plan.messageCount(units)),
		InputTokenCount:    summary.InputTokens,
		SummaryTokenCount:  summary.OutputTokens,
		ModelID:            summary.ModelID,
		PromptHash:         c.options.PromptHash,
	}

	// A later compaction summarizes the active summary plus the next raw
	// messages, so the new row starts where the old one did and supersedes it.
	// Ranges that overlap would otherwise compete during reconstruction.
	if c.active != nil {
		input.FromMessageID = c.active.FromMessageID
		input.FromSequence = c.active.FromSequence
		input.SourceMessageCount += c.active.SourceMessageCount
		input.SupersedesCompactionID = &c.active.ID
	}

	stored, err := c.options.Store.CreateCompaction(ctx, c.sessionID, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "store compaction")
	}

	return stored, nil
}

// replacePrefix returns the transcript with the covered units replaced by one
// synthetic summary message, leaving the leading system context and everything
// after the covered range exactly as they were.
func (c *compactor) replacePrefix(
	messages []elelem.Message,
	units int,
	content string,
) []elelem.Message {
	start := c.plan.summaryStart()
	tail := c.plan.LeadCount + c.plan.messageCount(units)

	next := make([]elelem.Message, 0, start+1+len(messages)-tail)
	next = append(next, messages[:start]...)
	next = append(next, compactionSummaryMessage(content))

	return append(next, messages[tail:]...)
}

// renderTranscript builds the summarizer's input: the active summary when
// there is one, then the raw messages this compaction covers.
func (c *compactor) renderTranscript(
	messages []elelem.Message,
	units int,
) string {
	start := c.plan.summaryStart()
	end := c.plan.LeadCount + c.plan.messageCount(units)

	builder := &strings.Builder{}
	builder.WriteString(compactionTranscriptOpenTag)
	builder.WriteString(compactionLineBreak)

	for _, message := range messages[start:end] {
		writeCompactionMessage(builder, message)
	}

	builder.WriteString(compactionTranscriptCloseTag)

	return builder.String()
}

func (c *compactor) summarize(
	ctx context.Context,
	transcript string,
) (compactionSummary, error) {
	if c.options.Summarize != nil {
		return c.options.Summarize(ctx, transcript)
	}

	return c.callModel(ctx, transcript)
}

// callModel runs the separate bounded summarization request.
//
// It carries no tools, no context budget, and no current user message. The
// missing budget is deliberate: a limit hook here is what would let a
// compaction trigger a compaction.
func (c *compactor) callModel(
	ctx context.Context,
	transcript string,
) (compactionSummary, error) {
	model, err := c.options.Models.ResolveModel(c.options.ModelReference)
	if err != nil {
		return compactionSummary{}, ctxerrors.Wrap(
			err,
			"resolve compaction model",
		)
	}

	prompt := elelem.NewPrompt().
		WithSystem(c.options.Prompt).
		UserText(transcript)

	audit, err := c.newCompactionAudit(ctx, model)
	if err != nil {
		return compactionSummary{}, ctxerrors.Wrap(
			err,
			"start compaction model audit",
		)
	}

	response, err := c.runCompactionRequest(ctx, model, prompt, audit)
	if err != nil {
		return compactionSummary{}, ctxerrors.Wrap(
			err,
			"run compaction request",
		)
	}

	return compactionSummary{
		Text:         response.Text,
		ModelID:      response.Model,
		InputTokens:  response.Usage.Prompt,
		OutputTokens: response.Usage.Completion,
	}, nil
}

func (c *compactor) runCompactionRequest(
	ctx context.Context,
	model ModelClient,
	prompt elelem.Prompt,
	audit *modelAuditRecorder,
) (*elelem.Response, error) {
	startedAt := time.Now()
	response, err := elelem.NewRequest(model.Client).
		WithModel(model.Model).
		WithPrompt(prompt).
		WithMaxOutputTokens(int64(c.options.MaxOutputTokens)).
		WithTimeout(c.options.Timeout).
		OnRoundStart(audit.onRoundStart).
		OnRoundEnd(audit.onRoundEnd).
		OnAssistantMessage(audit.onAssistantMessage).
		OnRetry(audit.onRetry).
		PreMaxTokensReached(rejectCompactionBudget).
		Run(ctx)

	persistCtx := context.WithoutCancel(ctx)
	if auditErr := audit.finish(persistCtx, response, err); auditErr != nil {
		err = errors.Join(err, auditErr)
	}

	observeModelRequest(
		c.options.Metrics,
		modelMetricStageCompaction,
		modelMetricFunctionCompaction,
		c.options.ModelReference,
		startedAt,
		time.Time{},
		response,
		err,
	)

	return response, err
}

func (c *compactor) newCompactionAudit(
	ctx context.Context,
	model ModelClient,
) (*modelAuditRecorder, error) {
	settings, err := newModelAuditSettings(
		model.Model,
		false,
		0,
		0,
		c.options.MaxOutputTokens,
		0,
		0,
		0,
		c.options.Timeout,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(
			err,
			"marshal compaction model request settings",
		)
	}

	audit, err := newModelAuditRecorder(ctx, modelAuditOptions{
		Store:               c.options.Store,
		SessionID:           c.sessionID,
		TurnID:              c.turnID,
		Stage:               models.ModelRunStageCompaction,
		ModelReference:      c.options.ModelReference,
		Model:               model.Model,
		RequestSettingsJSON: settings,
		Now:                 time.Now,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create compaction model audit")
	}

	return audit, nil
}

// rejectCompactionBudget refuses to compact the summarizer's own input.
//
// The request sets no WithMaxContextTokens, but the model still carries a
// context window, so Elelem would otherwise install DropOldestUnits here and
// quietly drop the oldest part of the prefix. That prefix would still be
// marked covered by the stored row, which is the exact silent loss this whole
// policy exists to prevent. Failing instead aborts the turn and changes no
// history.
func rejectCompactionBudget(
	_ context.Context,
	event *elelem.TokenLimitEvent,
) error {
	return ctxerrors.Wrapf(
		ErrCompactionUnavailable,
		"the prefix is %d tokens against a %d token summarizer budget",
		event.EstimatedTokens,
		event.BudgetTokens,
	)
}

// compactionSummaryMessage builds the synthetic history message.
//
// RoleUser, never RoleSystem: the text summarizes what the user and the model
// said, and promoting it to system authority would let summarized
// conversation outrank the deployment's own instructions.
func compactionSummaryMessage(content string) elelem.Message {
	return elelem.Message{
		Role:    elelem.RoleUser,
		Content: elelem.Text(content),
		Origin:  elelem.MessageOriginSeed,
	}
}

func writeCompactionMessage(
	builder *strings.Builder,
	message elelem.Message,
) {
	builder.WriteString(message.Role)
	builder.WriteString(compactionRoleSeparator)
	builder.WriteString(message.Text())
	builder.WriteString(compactionLineBreak)

	for _, call := range message.ToolCalls {
		builder.WriteString(compactionToolCallLead)
		builder.WriteString(call.Name)
		builder.WriteString(compactionArgumentSeparator)
		builder.WriteString(string(call.Arguments))
		builder.WriteString(compactionLineBreak)
	}
}
