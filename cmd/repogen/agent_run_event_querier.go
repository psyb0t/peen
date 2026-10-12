package main

import (
	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type AgentRunEventQuerier interface {
	/*
		SELECT question.*
		FROM @@table AS question
		INNER JOIN agent_runs AS run
			ON run.session_id = question.session_id
			AND run.id = question.agent_run_id
		WHERE question.session_id = @sessionID
		  AND run.state = 'running'
		  AND question.event_type = 'agent.run.question'
		  AND NOT EXISTS (
			  SELECT 1
			  FROM @@table AS answer
			  WHERE answer.agent_run_id = question.agent_run_id
			    AND answer.event_type = 'agent.run.answer'
			    AND answer.sequence > question.sequence
			    AND (
			      COALESCE(
			        json_extract(question.payload_json, '$.callId'), '') = ''
			      OR COALESCE(
			        json_extract(answer.payload_json, '$.callId'), '') = ''
			      OR json_extract(answer.payload_json, '$.callId') =
			        json_extract(question.payload_json, '$.callId')
			    )
		  )
		ORDER BY question.agent_run_id ASC, question.sequence ASC
	*/
	OpenQuestions(sessionID uuid.UUID) ([]*models.AgentRunEvent, error)
}
