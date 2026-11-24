package control

import (
	"context"

	"github.com/google/uuid"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

type (
	WorkspaceFileType    = api.WorkspaceFileType
	WorkspaceFileEntry   = api.WorkspaceFileEntry
	WorkspaceFileListing = api.WorkspaceFileListing
	WorkspaceFileContent = api.WorkspaceFileContent
)

// WorkspaceFileService provides reusable controller-side workspace file
// operations for a session.
type WorkspaceFileService interface {
	ListWorkspaceFiles(
		context.Context,
		uuid.UUID,
		string,
	) (WorkspaceFileListing, error)
	ReadWorkspaceFile(
		context.Context,
		uuid.UUID,
		string,
	) (WorkspaceFileContent, error)
	WriteWorkspaceFile(
		context.Context,
		uuid.UUID,
		string,
		string,
		bool,
	) (WorkspaceFileEntry, error)
	DeleteWorkspaceFile(context.Context, uuid.UUID, string) error
}

var _ WorkspaceFileService = (*Registry)(nil)
