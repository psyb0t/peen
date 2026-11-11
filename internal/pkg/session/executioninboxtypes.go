package session

import (
	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type ExecutionInboxPart struct {
	Content     string `json:"content"`
	SkillName   string `json:"skillName"`
	SkillIsNote bool   `json:"skillIsNote"`
}

type AcceptExecutionMessageInput struct {
	// AfterTurn retains input behind a recovered root without delivering it
	// into that root's model loop. The current turn lease is still required.
	AfterTurn bool `json:"afterTurn,omitempty"`

	ExpectedStopRevision *int64 `json:"expectedStopRevision,omitempty"`

	RequestID        uuid.UUID            `json:"requestId"`
	SourceEventID    uuid.UUID            `json:"sourceEventId,omitempty"`
	WorkingDirectory string               `json:"workingDirectory"`
	Message          string               `json:"message"`
	Parts            []ExecutionInboxPart `json:"parts"`
	// Images go with the message, stored in images_json when it is accepted
	// and shown to the model when it is delivered.
	Images   []MessageImage `json:"images,omitempty"`
	Capacity int            `json:"capacity"`
	// ResumeNotice is what a paused child reads ahead of this message: why
	// it was paused. A message to a paused child resumes it, so the notice is
	// queued only then, and ignored for a running child.
	ResumeNotice string `json:"resumeNotice,omitempty"`
}

type ExecutionInboxPage struct {
	Items   []*models.ExecutionInboxMessage `json:"items"`
	Limit   int                             `json:"limit"`
	Offset  int                             `json:"offset"`
	HasMore bool                            `json:"hasMore"`
}

type ExecutionInboxAcceptance struct {
	Message *models.ExecutionInboxMessage `json:"message"`
	Events  []EventInput                  `json:"events"`
	// Resumed lists the paused executions the message resumed, so the caller
	// can start their workflows again.
	Resumed []uuid.UUID `json:"resumed,omitempty"`
}

type ExecutionInboxDelivery struct {
	Messages []MessageInput `json:"messages"`
	Events   []EventInput   `json:"events"`
}

type ListExecutionInboxOptions struct {
	State  models.InboxMessageState `json:"state,omitempty"`
	Limit  int                      `json:"limit"`
	Offset int                      `json:"offset"`
}

type DeliverExecutionMessagesInput struct {
	TurnLease  Lease         `json:"turnLease"`
	ModelLease ActivityLease `json:"modelLease"`
	RequestIDs []uuid.UUID   `json:"requestIds"`
}

// inboxEncoding is an accepted message's parts_json and the images it
// carries, ready to store.
type inboxEncoding struct {
	parts  string
	images preparedImages
}

// emptyInboxEncoding is the encoding of a message with no parts and no
// images, such as a resume notice or a child report.
func emptyInboxEncoding() inboxEncoding {
	return inboxEncoding{
		parts:  emptyChildReportPartsJSON,
		images: preparedImages{json: emptyImagesJSON},
	}
}

type executionInboxMessagePayload struct {
	Message       string             `json:"message"`
	SourceEventID string             `json:"sourceEventId,omitempty"`
	Images        []ImageDescription `json:"images,omitempty"`
}
