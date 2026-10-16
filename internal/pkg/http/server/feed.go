package server

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// agentRunFeedFrameData is the live frame payload for an agent.run.* feed
// item, exactly as docs/http-api.md documents it.
type agentRunFeedFrameData struct {
	AgentRunID       uuid.UUID       `json:"agentRunId"`
	ParentTurnID     uuid.UUID       `json:"parentTurnId"`
	ParentAgentRunID *uuid.UUID      `json:"parentAgentRunId,omitempty"`
	Event            json.RawMessage `json:"event"`
}

// feedItemFrame renders one stored feed row as the live WebSocket frame for
// it. BroadcastFeedItems and ListSessionFeed both call this, so a replayed
// row and the frame a connected client saw for it can never read differently.
//
// The id is the stored row's id rather than the random one NewEvent assigns,
// the timestamp is the row's createdAt rather than render time, and the
// metadata carries sequence so a client can track its cursor from either
// path.
func feedItemFrame(item session.FeedItem) (*dabluveees.Event, error) {
	data, err := feedItemFrameData(item)
	if err != nil {
		return nil, err
	}

	rendered := dabluveees.NewEvent(dabluveees.EventType(item.EventType), data).
		SetMetadata(webSocketMetadataSessionID, item.SessionID.String()).
		SetMetadata(webSocketMetadataSequence, item.Sequence)

	if item.RequestID != uuid.Nil {
		rendered = rendered.SetMetadata(
			webSocketMetadataRequestID,
			item.RequestID.String(),
		)
	}

	rendered.ID = item.ID
	rendered.Timestamp = item.CreatedAt.Unix()
	rendered.TriggeredBy = nil

	return &rendered, nil
}

func feedItemFrameData(item session.FeedItem) (any, error) {
	switch item.Kind {
	case session.FeedKindEvent:
		return json.RawMessage(item.PayloadJSON), nil
	case session.FeedKindAgentRunEvent:
		return agentRunFeedFrameData{
			AgentRunID:       item.AgentRunID,
			ParentTurnID:     item.ParentTurnID,
			ParentAgentRunID: item.ParentAgentRunID,
			Event:            json.RawMessage(item.PayloadJSON),
		}, nil
	case session.FeedKindNotice:
		notice, err := agent.SessionNoticeModelToAPI(item.Notice)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "render notice feed frame")
		}

		return notice, nil
	default:
		return nil, ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"unknown feed item kind %q",
			item.Kind,
		)
	}
}

// BroadcastFeedItems fans out feed rows the store has already committed, in
// the order the pump delivered them.
//
// It is the live half of the feed path: a write lands in SQLite first, the
// store signals the pump, and only then does a page of rows reach here for
// delivery. A client therefore never sees a row the database does not already
// hold.
func (s *Server) BroadcastFeedItems(
	ctx context.Context,
	sessionID uuid.UUID,
	items []session.FeedItem,
) {
	logger := ctxscope.GetLogger(ctx)

	for _, item := range items {
		rendered, err := feedItemFrame(item)
		if err != nil {
			logger.Error(
				"feed item render failed",
				"err", ctxerrors.Wrap(err, "render feed item"),
				"session_id", sessionID.String(),
				"sequence", item.Sequence,
			)

			continue
		}

		s.broadcastWebSocketEvent(ctx, sessionID, rendered)
	}
}

// ListSessionFeed replays a session's feed after cursor, through the same
// renderer the live frames use.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionFeed(
	ctx context.Context,
	request api.ListSessionFeedRequestObject,
) (api.ListSessionFeedResponseObject, error) {
	page, limit, err := s.deps.Runtime.ListSessionFeed(ctx, request.Params)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionFeed404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					sessionNotFoundError(),
				),
			}, nil
		}

		if errors.Is(err, commerr.ErrValidationFailed) {
			return api.ListSessionFeed400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError(clientMessage(err)),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session feed")
	}

	body, err := feedPageToAPI(page, limit)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "render session feed page")
	}

	return api.ListSessionFeed200JSONResponse{
		Body: body,
		Headers: api.ListSessionFeed200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

func feedPageToAPI(page *session.FeedPage, limit int32) (api.FeedPage, error) {
	items := make([]api.FeedFrame, 0, len(page.Items))

	for _, item := range page.Items {
		frame, err := feedFrameToAPI(item)
		if err != nil {
			return api.FeedPage{}, err
		}

		items = append(items, frame)
	}

	return api.FeedPage{
		Items:      items,
		Limit:      limit,
		NextCursor: page.NextCursor,
		HasMore:    page.HasMore,
		Head:       page.Head,
	}, nil
}

func feedFrameToAPI(item session.FeedItem) (api.FeedFrame, error) {
	rendered, err := feedItemFrame(item)
	if err != nil {
		return api.FeedFrame{}, err
	}

	var data any
	if len(rendered.Data) > 0 {
		if err := json.Unmarshal(rendered.Data, &data); err != nil {
			return api.FeedFrame{}, ctxerrors.Wrap(
				err,
				"decode session feed frame data",
			)
		}
	}

	frame := api.FeedFrame{
		Sequence:  item.Sequence,
		Id:        item.ID,
		Type:      string(rendered.Type),
		SessionId: item.SessionID,
		Data:      data,
		CreatedAt: item.CreatedAt,
	}

	if item.RequestID != uuid.Nil {
		requestID := item.RequestID
		frame.RequestId = &requestID
	}

	return frame, nil
}
