package peen

import (
	"context"

	"github.com/psyb0t/peen/internal/pkg/http/api"
)

func pageParameters(request PageRequest) (*int32, *int32, error) {
	limit, err := toBoundedInt32(request.Limit, "limit")
	if err != nil {
		return nil, nil, err
	}

	offset, err := toBoundedInt32(request.Offset, "offset")
	if err != nil {
		return nil, nil, err
	}

	if limit == 0 {
		return nil, &offset, nil
	}

	return &limit, &offset, nil
}

//nolint:ireturn // T is the caller-selected owned record type.
func sessionRead[T any](
	ctx context.Context,
	s *Session,
	read func(context.Context) (any, error),
) (T, error) {
	var zero T

	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return zero, err
	}
	defer finish()

	return convertRecord[T](read(ctx))
}

// Turns reads the durable turn history, newest first.
func (s *Session) Turns(
	ctx context.Context,
	page PageRequest,
) (TurnPage, error) {
	limit, offset, err := pageParameters(page)
	if err != nil {
		return TurnPage{}, err
	}

	read := func(ctx context.Context) (any, error) {
		return s.core().ListSessionTurns(ctx,
			api.ListSessionTurnsParams{
				XSessionID: s.id, Limit: limit, Offset: offset,
			})
	}

	return sessionRead[TurnPage](ctx, s, read)
}

// Events reads persisted protocol events without consuming notices.
func (s *Session) Events(
	ctx context.Context, page PageRequest,
) (TranscriptEventPage, error) {
	limit, offset, err := pageParameters(page)
	if err != nil {
		return TranscriptEventPage{}, err
	}

	return sessionRead[TranscriptEventPage](ctx, s,
		func(ctx context.Context) (any, error) {
			return s.core().ListSessionEvents(ctx,
				api.ListSessionEventsParams{
					XSessionID: s.id, Limit: limit, Offset: offset,
				})
		})
}

// Compactions reads the root conversation's immutable summary lineage.
func (s *Session) Compactions(
	ctx context.Context, page PageRequest,
) (CompactionPage, error) {
	limit, offset, err := pageParameters(page)
	if err != nil {
		return CompactionPage{}, err
	}

	return sessionRead[CompactionPage](ctx, s,
		func(ctx context.Context) (any, error) {
			return s.core().ListSessionCompactions(ctx,
				api.ListSessionCompactionsParams{
					XSessionID: s.id, Limit: limit, Offset: offset,
				})
		})
}

// ModelRuns reads provider work and its usage and cost records.
func (s *Session) ModelRuns(
	ctx context.Context, page PageRequest,
) (ModelRunPage, error) {
	limit, offset, err := pageParameters(page)
	if err != nil {
		return ModelRunPage{}, err
	}

	return sessionRead[ModelRunPage](ctx, s,
		func(ctx context.Context) (any, error) {
			return s.core().ListSessionModelRuns(ctx,
				api.ListSessionModelRunsParams{
					XSessionID: s.id, Limit: limit, Offset: offset,
				})
		})
}

// ModelRunCalls reads individual provider rounds belonging to this session.
func (s *Session) ModelRunCalls(
	ctx context.Context,
	runID string,
	page PageRequest,
) (ModelCallPage, error) {
	id, err := parseRecordID(runID)
	if err != nil {
		return ModelCallPage{}, err
	}

	limit, offset, err := pageParameters(page)
	if err != nil {
		return ModelCallPage{}, err
	}

	return sessionRead[ModelCallPage](ctx, s,
		func(ctx context.Context) (any, error) {
			return s.core().ListSessionModelRunCalls(ctx, s.id, id,
				api.ListSessionModelRunCallsParams{
					XSessionID: s.id, Limit: limit, Offset: offset,
				})
		})
}

// Notices reads external, job and child-agent reports without consuming them.
func (s *Session) Notices(
	ctx context.Context, page PageRequest,
) (SessionNoticePage, error) {
	limit, offset, err := pageParameters(page)
	if err != nil {
		return SessionNoticePage{}, err
	}

	return sessionRead[SessionNoticePage](ctx, s,
		func(ctx context.Context) (any, error) {
			return s.core().ListSessionNotices(ctx,
				api.ListSessionNoticesParams{
					XSessionID: s.id, Limit: limit, Offset: offset,
				})
		})
}

// Compaction reads a summary only when it belongs to this session.
func (s *Session) Compaction(
	ctx context.Context,
	compactionID string,
) (Compaction, error) {
	id, err := parseRecordID(compactionID)
	if err != nil {
		return Compaction{}, err
	}

	read := func(ctx context.Context) (any, error) {
		return s.core().GetSessionCompaction(ctx, s.id, id)
	}

	return sessionRead[Compaction](ctx, s, read)
}

// ContextSnapshot reads a harness snapshot referenced by this session.
func (s *Session) ContextSnapshot(
	ctx context.Context,
	hash string,
) (ContextSnapshot, error) {
	read := func(ctx context.Context) (any, error) {
		return s.core().GetSessionContextSnapshot(ctx, s.id, hash)
	}

	return sessionRead[ContextSnapshot](ctx, s, read)
}

// PromptSnapshot reads an effective prompt referenced by this session.
func (s *Session) PromptSnapshot(
	ctx context.Context,
	hash string,
) (PromptSnapshot, error) {
	read := func(ctx context.Context) (any, error) {
		return s.core().GetSessionPromptSnapshot(ctx, s.id, hash)
	}

	return sessionRead[PromptSnapshot](ctx, s, read)
}
