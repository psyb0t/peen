package main

import (
	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type AgentRunCompactionQuerier interface {
	/*
		SELECT current.*
		FROM agent_run_compactions AS current
		WHERE current.agent_run_id = @agentRunID
		  AND NOT EXISTS (
			  SELECT 1
			  FROM agent_run_compactions AS newer
			  WHERE newer.agent_run_id = current.agent_run_id
			    AND newer.supersedes_compaction_id = current.id
		  )
		ORDER BY current.to_sequence DESC, current.id DESC
		LIMIT 1
	*/
	ActiveHead(agentRunID uuid.UUID) (*models.AgentRunCompaction, error)
}
