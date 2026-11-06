package metrics

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// SummaryKind names one fixed aggregation of stored observations. The set is
// closed: an agent reading metrics picks one of these and never supplies SQL.
type SummaryKind string

const (
	SummaryTools  SummaryKind = "tools"
	SummaryModels SummaryKind = "models"
	SummaryTurns  SummaryKind = "turns"
	SummaryAgents SummaryKind = "agents"
	SummaryHooks  SummaryKind = "hooks"
	// SummarySessions groups model time by session, which says something only
	// to a reader that covers every session.
	SummarySessions SummaryKind = "sessions"

	// MaxSummaryRows bounds one result, because it goes into a model's context.
	MaxSummaryRows = 100

	defaultSummaryRows          = 20
	defaultSummaryWindowMinutes = 60
)

// SummaryQuery asks for one aggregation over a recent window.
type SummaryQuery struct {
	Summary       SummaryKind `json:"summary"`
	WindowMinutes int         `json:"windowMinutes"`
	Limit         int         `json:"limit"`
}

// SummaryRow aggregates the observations that share one name. Which name that
// is depends on the summary: a tool, a model, a turn ID, or a child agent run.
type SummaryRow struct {
	Name             string `json:"name"`
	Count            int64  `json:"count"`
	Failures         int64  `json:"failures"`
	Cancelled        int64  `json:"cancelled"`
	TotalDurationMS  int64  `json:"totalDurationMs"`
	MaxDurationMS    int64  `json:"maxDurationMs"`
	InputTokens      int64  `json:"inputTokens,omitempty"`
	OutputTokens     int64  `json:"outputTokens,omitempty"`
	CacheReadTokens  int64  `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int64  `json:"cacheWriteTokens,omitempty"`
	Retries          int64  `json:"retries,omitempty"`
}

// SummaryResult is one aggregation, slowest name first. Truncated says rows
// were left out to stay within the query's limit.
type SummaryResult struct {
	Summary       SummaryKind  `json:"summary"`
	WindowMinutes int          `json:"windowMinutes"`
	Rows          []SummaryRow `json:"rows"`
	Truncated     bool         `json:"truncated"`
}

// SummaryScope is the set of sessions a read covers. The controller sets it
// from the connection, and a worker never supplies it.
type SummaryScope struct {
	SessionID   string
	AllSessions bool
}

// SummaryReader answers a query over stored observations.
type SummaryReader interface {
	Summarize(
		ctx context.Context,
		scope SummaryScope,
		query SummaryQuery,
	) (SummaryResult, error)
}

// SetSummaries installs where a worker's metrics queries go. Nil, the state
// when metrics.db is unavailable, makes each query fail.
func (m *Metrics) SetSummaries(reader SummaryReader) {
	if m == nil {
		return
	}

	if reader == nil {
		m.summaries.Store(nil)

		return
	}

	m.summaries.Store(&reader)
}

// ReadAllSessions widens this connection's reads to every session. Only the
// system worker's connection gets it.
func (i *WorkerIngest) ReadAllSessions() {
	if i == nil {
		return
	}

	i.allSessions = true
}

// Summarize answers a worker's query. The scope comes from this connection,
// so an ordinary worker reads only the session it authenticated.
func (i *WorkerIngest) Summarize(
	ctx context.Context,
	query SummaryQuery,
) (SummaryResult, error) {
	if i == nil || i.metrics == nil {
		return SummaryResult{}, errSummariesUnavailable()
	}

	reader := i.metrics.summaries.Load()
	if reader == nil {
		return SummaryResult{}, errSummariesUnavailable()
	}

	normalized, err := query.normalized()
	if err != nil {
		return SummaryResult{}, err
	}

	result, err := (*reader).Summarize(
		ctx,
		SummaryScope{SessionID: i.sessionID, AllSessions: i.allSessions},
		normalized,
	)
	if err != nil {
		return SummaryResult{}, ctxerrors.Wrap(err, "summarize observations")
	}

	return result, nil
}

func errSummariesUnavailable() error {
	return ctxerrors.Wrap(
		commerr.ErrNotImplemented,
		"stored metrics are unavailable",
	)
}

func (q SummaryQuery) normalized() (SummaryQuery, error) {
	switch q.Summary {
	case SummaryTools,
		SummaryModels,
		SummaryTurns,
		SummaryAgents,
		SummaryHooks,
		SummarySessions:
	default:
		return SummaryQuery{}, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"unknown metrics summary %q",
			q.Summary,
		)
	}

	if q.WindowMinutes < 0 || q.Limit < 0 {
		return SummaryQuery{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"windowMinutes and limit must not be negative",
		)
	}

	if q.WindowMinutes == 0 {
		q.WindowMinutes = defaultSummaryWindowMinutes
	}

	if q.Limit == 0 {
		q.Limit = defaultSummaryRows
	}

	q.Limit = min(q.Limit, MaxSummaryRows)

	return q, nil
}
