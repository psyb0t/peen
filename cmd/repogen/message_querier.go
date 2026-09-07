package main

import (
	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type MessageQuerier interface {
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
