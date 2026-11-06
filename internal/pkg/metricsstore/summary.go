package metricsstore

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"gorm.io/gen/field"
)

// rootAgentName labels the observations of the root conversation, which has
// no child agent run.
const rootAgentName = "root"

// outcomeTotals is one name and outcome's aggregate. Grouping by outcome and
// folding in Go keeps the query inside the typed builder.
type outcomeTotals struct {
	Name             string
	Outcome          string
	Count            int64
	TotalDurationMs  int64
	MaxDurationMs    int64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	Retries          int64
}

// Summarize aggregates recent observations in scope. It writes what is queued
// first, so a caller sees what it just reported.
func (s *Store) Summarize(
	ctx context.Context,
	scope metrics.SummaryScope,
	query metrics.SummaryQuery,
) (metrics.SummaryResult, error) {
	s.writeQueued(ctx)

	repository := s.storage.Query.MetricObservation

	group, kind, err := summaryGrouping(
		query.Summary,
		repository.Name,
		repository.TurnID,
		repository.AgentRunID,
		repository.SessionID,
	)
	if err != nil {
		return metrics.SummaryResult{}, err
	}

	var totals []outcomeTotals

	selection := repository.WithContext(ctx).
		Where(
			repository.Kind.Eq(string(kind)),
			repository.ObservedAtMs.Gte(summaryCutoff(query)),
		)
	if !scope.AllSessions {
		selection = selection.Where(repository.SessionID.Eq(scope.SessionID))
	}

	err = selection.
		Select(
			group.As("name"),
			repository.Outcome,
			repository.ID.Count().As("count"),
			repository.DurationMs.Sum().As("total_duration_ms"),
			repository.DurationMs.Max().As("max_duration_ms"),
			repository.InputTokens.Sum().As("input_tokens"),
			repository.OutputTokens.Sum().As("output_tokens"),
			repository.CacheReadTokens.Sum().As("cache_read_tokens"),
			repository.CacheWriteTokens.Sum().As("cache_write_tokens"),
			repository.Retries.Sum().As("retries"),
		).
		Group(group, repository.Outcome).
		Scan(&totals)
	if err != nil {
		return metrics.SummaryResult{}, ctxerrors.Wrap(
			err, "read metric summary",
		)
	}

	rows := foldOutcomes(totals, query.Summary)

	return metrics.SummaryResult{
		Summary:       query.Summary,
		WindowMinutes: query.WindowMinutes,
		Rows:          rows[:min(len(rows), query.Limit)],
		Truncated:     len(rows) > query.Limit,
	}, nil
}

func summaryCutoff(query metrics.SummaryQuery) int64 {
	window := time.Duration(query.WindowMinutes) * time.Minute

	return time.Now().Add(-window).UnixMilli()
}

func summaryGrouping(
	summary metrics.SummaryKind,
	name, turnID, agentRunID, sessionID field.String,
) (field.String, metrics.ObservationKind, error) {
	switch summary {
	case metrics.SummaryTools:
		return name, metrics.KindTool, nil
	case metrics.SummaryModels:
		return name, metrics.KindModel, nil
	case metrics.SummaryTurns:
		return turnID, metrics.KindModel, nil
	case metrics.SummaryAgents:
		return agentRunID, metrics.KindModel, nil
	case metrics.SummaryHooks:
		return name, metrics.KindHook, nil
	case metrics.SummarySessions:
		return sessionID, metrics.KindModel, nil
	default:
		return field.String{}, "", ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"unknown metrics summary %q",
			summary,
		)
	}
}

// foldOutcomes merges each name's outcomes into one row, slowest name first.
func foldOutcomes(
	totals []outcomeTotals,
	summary metrics.SummaryKind,
) []metrics.SummaryRow {
	rows := map[string]*metrics.SummaryRow{}

	for _, total := range totals {
		name := total.Name
		if name == "" && summary == metrics.SummaryAgents {
			name = rootAgentName
		}

		row := rows[name]
		if row == nil {
			row = &metrics.SummaryRow{Name: name}
			rows[name] = row
		}

		addOutcome(row, total)
	}

	ordered := make([]metrics.SummaryRow, 0, len(rows))
	for _, row := range rows {
		ordered = append(ordered, *row)
	}

	slices.SortFunc(ordered, func(left, right metrics.SummaryRow) int {
		return cmp.Or(
			cmp.Compare(right.TotalDurationMS, left.TotalDurationMS),
			cmp.Compare(left.Name, right.Name),
		)
	})

	return ordered
}

func addOutcome(row *metrics.SummaryRow, total outcomeTotals) {
	row.Count += total.Count
	row.TotalDurationMS += total.TotalDurationMs
	row.MaxDurationMS = max(row.MaxDurationMS, total.MaxDurationMs)
	row.InputTokens += total.InputTokens
	row.OutputTokens += total.OutputTokens
	row.CacheReadTokens += total.CacheReadTokens
	row.CacheWriteTokens += total.CacheWriteTokens
	row.Retries += total.Retries

	switch total.Outcome {
	case metrics.OutcomeError:
		row.Failures += total.Count
	case metrics.OutcomeCancelled:
		row.Cancelled += total.Count
	default:
	}
}
