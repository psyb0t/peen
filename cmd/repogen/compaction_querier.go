package main

import (
	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type CompactionQuerier interface {
	/*
		SELECT current.*
		FROM compactions AS current
		WHERE current.session_id = @sessionID
		  AND NOT EXISTS (
			  SELECT 1
			  FROM compactions AS newer
			  WHERE newer.session_id = current.session_id
			    AND newer.supersedes_compaction_id = current.id
		  )
		ORDER BY current.to_sequence DESC, current.id DESC
		LIMIT 1
	*/
	ActiveHead(sessionID uuid.UUID) (*models.Compaction, error)
}
