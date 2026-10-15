package main

import (
	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type ExecutionInboxMessageQuerier interface {
	/*
		SELECT inbox.* FROM @@table AS inbox
		INNER JOIN executions AS source
		  ON source.id = inbox.execution_id
		  AND source.session_id = inbox.session_id
		WHERE inbox.session_id = @sessionID AND inbox.state = @state
		  AND source.state = @sourceState AND source.kind = @sourceKind
		ORDER BY source.created_at ASC, source.id ASC,
		         inbox.sequence ASC, inbox.id ASC
		LIMIT @limit OFFSET @offset
	*/
	Deferred(
		sessionID uuid.UUID,
		state, sourceState, sourceKind string,
		limit, offset int,
	) ([]*models.ExecutionInboxMessage, error)

	/*
		SELECT COALESCE(SUM(1 + json_array_length(parts_json)), 0)
		FROM @@table
		WHERE session_id = @sessionID AND execution_id = @executionID
		  AND state = @state
	*/
	QueuedSlots(sessionID, executionID, state string) (int64, error)

	/*
		SELECT COUNT(*) FROM @@table AS inbox
		WHERE inbox.session_id = @sessionID
		  AND inbox.execution_id = @executionID AND inbox.state = @state
		  AND NOT (
		    inbox.sequence > @cutoff
		    AND inbox.source_event_id IS NOT NULL
		    AND inbox.request_id = inbox.source_event_id
		    AND EXISTS (
		      SELECT 1 FROM agent_run_events AS event
		      INNER JOIN executions AS child
		        ON child.agent_run_id = event.agent_run_id
		        AND child.session_id = event.session_id
		      WHERE event.id = inbox.source_event_id
		        AND event.session_id = inbox.session_id
		        AND event.event_type IN (@completed, @failed)
		        AND child.parent_execution_id = inbox.execution_id
		    )
		  )
	*/
	CompletionBlockers(
		sessionID, executionID, state string,
		cutoff int64,
		completed, failed string,
	) (int64, error)
}
