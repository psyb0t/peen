package models

import (
	"time"

	"github.com/google/uuid"
)

// WorkerGeneration is one live worker process for one session and one profile.
//
// It is recorded before the worker launches, so a controller that dies
// mid-launch still leaves a row an operator can reconcile. Replacing a dead
// worker starts a new generation rather than reusing the old one, so every
// turn can name the exact process that ran it.
type WorkerGeneration struct {
	ID              uuid.UUID
	SessionID       uuid.UUID
	Kind            string
	Profile         string
	ProfileRevision int64
	State           string
	Workspace       string

	// CredentialHash verifies the worker's one-time launch credential. The raw
	// credential is never stored, so a controller that reads its own database
	// cannot recover one, and a restarted controller can still accept the
	// worker it recorded.
	CredentialHash string

	// SocketPath is the private session-bound socket this generation talks to
	// its controller over.
	SocketPath string

	// ProcessID identifies a native worker. It is zero for a Docker worker.
	ProcessID int

	// ContainerID and ImageDigest identify a Docker worker.
	ContainerID string
	ImageDigest string

	// FailureDetail explains a failed generation. It never holds a credential.
	FailureDetail string

	// ExitCode is the process or container exit status once the worker ended.
	ExitCode int

	CreatedAt time.Time
	StartedAt *time.Time
	EndedAt   *time.Time
}

// TableName pins the schema-owned worker_generations table name.
func (WorkerGeneration) TableName() string {
	return "worker_generations"
}
