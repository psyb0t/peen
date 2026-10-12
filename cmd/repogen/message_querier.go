package main

import (
	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type MessageQuerier interface {
	/*
		SELECT result.workspace, result.sequence
		FROM @@table AS result
		WHERE result.session_id = @sessionID
		  AND result.role = 'tool'
		  AND result.is_error = FALSE
		  AND result.workspace <> ''
		  AND 'changeDirectory' = (
		    SELECT COALESCE(json_extract(entry.value, '$.Name'),
		      json_extract(entry.value, '$.name'))
		    FROM @@table AS call, json_each(call.tool_calls_json) AS entry
		    WHERE call.session_id = result.session_id
		      AND call.turn_id = result.turn_id
		      AND call.role = 'assistant'
		      AND call.sequence < result.sequence
		      AND CASE WHEN entry.type = 'object'
		        THEN COALESCE(json_extract(entry.value, '$.ID'),
		          json_extract(entry.value, '$.id')) END = result.tool_call_id
		    ORDER BY call.sequence DESC
		    LIMIT 1
		  )
		ORDER BY result.sequence DESC
		LIMIT 1
	*/
	LatestDirectoryChange(sessionID uuid.UUID) ([]*models.Message, error)

	/*
		SELECT m.*
		FROM messages AS m
		INNER JOIN turns
			ON turns.session_id = m.session_id
			AND turns.id = m.turn_id
		WHERE m.session_id = @sessionID
		  AND turns.state = 'completed'
		  AND m.sequence > @afterSequence
		ORDER BY m.sequence ASC, m.id ASC
	*/
	CompletedHistory(
		sessionID uuid.UUID,
		afterSequence int64,
	) ([]*models.Message, error)
}
