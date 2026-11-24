package openaicompat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (s *streamWriter) observe(
	ctx context.Context,
	structured bool,
) func(session.FeedItem) error {
	return func(item session.FeedItem) error {
		if err := ctx.Err(); err != nil {
			return ctxerrors.Wrap(err, "OpenAI stream cancelled")
		}

		if item.Kind != session.FeedKindEvent {
			return nil
		}

		if item.SessionID == uuid.Nil {
			return ctxerrors.Wrap(commerr.ErrInvalidState,
				"OpenAI stream event has no session")
		}

		if !s.started {
			s.response.Header().Set(headerSessionID, item.SessionID.String())
		}

		if structured || item.EventType != contentBlockDelta {
			return nil
		}

		var event streamEvent

		if err := json.Unmarshal([]byte(item.PayloadJSON), &event); err != nil {
			return ctxerrors.Wrap(commerr.ErrParseFailed,
				"decode OpenAI stream delta")
		}

		if event.Delta.Type != textDelta || event.Delta.Text == "" {
			return nil
		}

		return s.text(event.Delta.Text)
	}
}

func (s *streamWriter) text(text string) error {
	if !s.started {
		if err := s.write(chunkChoice{
			Delta: chunkDelta{Role: roleAssistant},
		}, nil, nil); err != nil {
			return err
		}
	}

	s.wroteText = true

	return s.write(chunkChoice{Delta: chunkDelta{Content: &text}}, nil, nil)
}

func (s *streamWriter) finish(
	text string,
	result *control.ConversationResult,
) error {
	if !s.wroteText && text != "" {
		if err := s.text(text); err != nil {
			return err
		}
	}

	finish := resultFinishReason(result)
	usage := resultUsage(result)

	if err := s.write(chunkChoice{FinishReason: &finish}, &usage,
		nil); err != nil {
		return err
	}

	return s.done()
}

func (s *streamWriter) fail(status int, problem responseError) error {
	if !s.started {
		return writeJSON(s.response, status, errorEnvelope{Error: problem})
	}

	finish := finishError
	if err := s.write(chunkChoice{FinishReason: &finish}, nil,
		&errorEnvelope{Error: problem}); err != nil {
		return err
	}

	return s.done()
}

func (s *streamWriter) write(
	choice chunkChoice,
	usage *usage,
	extension *errorEnvelope,
) error {
	payload, err := json.Marshal(chunk{
		ID: s.id, Object: objectChunk, Created: s.created, Model: s.model,
		Choices: []chunkChoice{choice}, Usage: usage, Extension: extension,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "encode OpenAI stream chunk")
	}

	if !s.started {
		s.response.Header().Set(aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeTextEventStream)
		s.response.Header().Set(aichteeteapee.HeaderNameCacheControl,
			cacheControlNoCache)
		s.started = true
	}

	return s.frame(string(payload))
}

func (s *streamWriter) done() error {
	return s.frame(sseDone)
}

func (s *streamWriter) frame(payload string) error {
	if _, err := io.WriteString(s.response,
		ssePrefix+payload+sseEnd); err != nil {
		return ctxerrors.Wrap(err, "write OpenAI stream frame")
	}

	if err := http.NewResponseController(s.response).Flush(); err != nil {
		return ctxerrors.Wrap(err, "flush OpenAI stream frame")
	}

	return nil
}
