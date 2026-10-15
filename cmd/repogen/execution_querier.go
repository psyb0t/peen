package main

import (
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type ExecutionQuerier interface {
	/*
		WITH RECURSIVE tree(id) AS (
			SELECT id FROM @@table
			WHERE session_id = @sessionID AND id = @executionID
			UNION
			SELECT child.id FROM @@table AS child
			INNER JOIN tree ON child.parent_execution_id = tree.id
			WHERE child.session_id = @sessionID
		)
		SELECT execution.id, execution.session_id, execution.state,
		       execution.workflow_execution_id, execution.event_count,
		       execution.control_revision
		FROM @@table AS execution
		INNER JOIN tree ON tree.id = execution.id
		WHERE execution.session_id = @sessionID
		ORDER BY execution.created_at ASC, execution.id ASC
	*/
	Subtree(sessionID, executionID uuid.UUID) ([]*models.Execution, error)

	/*
		SELECT execution.* FROM @@table AS execution
		WHERE execution.session_id = @sessionID
		  AND (@executionID = '' OR execution.id = @executionID)
		  AND (@parentID = '' OR execution.parent_execution_id = @parentID)
		  AND (@rootID = '' OR execution.root_execution_id = @rootID)
		  AND (@state = '' OR execution.state = @state)
		  AND (@fromTime IS NULL OR execution.created_at >= @fromTime)
		  AND (@toTime IS NULL OR execution.created_at < @toTime)
		  AND (@text = '' OR
		    instr(lower(execution.task), lower(@text)) > 0 OR
		    instr(lower(execution.context_json), lower(@text)) > 0 OR
		    instr(lower(execution.pause_reason), lower(@text)) > 0 OR
		    EXISTS (
		      SELECT 1 FROM execution_events AS event
		      WHERE event.session_id = execution.session_id
		        AND event.execution_id = execution.id
		        AND instr(lower(event.payload_json), lower(@text)) > 0
		    ))
		ORDER BY execution.created_at DESC, execution.id DESC
		LIMIT @limit OFFSET @offset
	*/
	Search(
		sessionID uuid.UUID,
		executionID, parentID, rootID, state, text string,
		fromTime, toTime *time.Time,
		limit, offset int,
	) ([]*models.Execution, error)
}
