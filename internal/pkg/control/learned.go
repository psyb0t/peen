package control

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// ListLearnedFiles lists the learned rules and skills the session's workspace
// and the global learned directory hold.
func (r *Registry) ListLearnedFiles(
	ctx context.Context,
	sessionID uuid.UUID,
) (api.LearnedFileList, error) {
	directories, err := r.learnedDirectories(ctx, sessionID)
	if err != nil {
		return api.LearnedFileList{}, err
	}

	files, err := harness.ListLearnedFiles(directories, estimateLearnedTokens)
	if err != nil {
		return api.LearnedFileList{}, ctxerrors.Wrap(err,
			"list learned files")
	}

	items := make([]api.LearnedFile, 0, len(files))
	for _, file := range files {
		items = append(items, learnedFileToAPI(file))
	}

	return api.LearnedFileList{Items: items}, nil
}

// ReadLearnedFile returns one learned rule or skill and its content.
func (r *Registry) ReadLearnedFile(
	ctx context.Context,
	sessionID uuid.UUID,
	reference harness.LearnedFileReference,
) (api.LearnedFileContent, error) {
	directories, err := r.learnedDirectories(ctx, sessionID)
	if err != nil {
		return api.LearnedFileContent{}, err
	}

	file, content, err := harness.ReadLearnedFile(
		directories,
		reference,
		estimateLearnedTokens,
	)
	if err != nil {
		return api.LearnedFileContent{}, ctxerrors.Wrap(err,
			"read learned file")
	}

	return api.LearnedFileContent{
		File:    learnedFileToAPI(file),
		Content: content,
	}, nil
}

// DeleteLearnedFile removes one learned rule or skill. The next harness
// resolution no longer loads it.
func (r *Registry) DeleteLearnedFile(
	ctx context.Context,
	sessionID uuid.UUID,
	reference harness.LearnedFileReference,
) error {
	directories, err := r.learnedDirectories(ctx, sessionID)
	if err != nil {
		return err
	}

	if err := harness.DeleteLearnedFile(directories, reference); err != nil {
		return ctxerrors.Wrap(err, "delete learned file")
	}

	ctxscope.GetLogger(ctx).Info(
		"learned file deleted",
		"session_id", sessionID.String(),
		"scope", string(reference.Scope),
		"kind", string(reference.Kind),
		"name", reference.Name,
	)

	return nil
}

func (r *Registry) learnedDirectories(
	ctx context.Context,
	sessionID uuid.UUID,
) (harness.LearnedDirectories, error) {
	stored, err := r.store.Get(ctx, sessionID)
	if err != nil {
		return harness.LearnedDirectories{}, ctxerrors.Wrap(err,
			"load learned file session")
	}

	return harness.LearnedDirectories{
		Workspace: stored.Workspace,
		Global:    r.learnedDirectory,
	}, nil
}

func estimateLearnedTokens(text string) (int, error) {
	tokens, err := elelem.DefaultTokenCounter().Count([]elelem.Message{{
		Role:    elelem.RoleUser,
		Content: elelem.Text(text),
	}}, nil)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "count learned file tokens")
	}

	return tokens, nil
}

func learnedFileToAPI(file harness.LearnedFile) api.LearnedFile {
	return api.LearnedFile{
		Scope:           api.LearnedScope(file.Scope),
		Kind:            api.LearnedKind(file.Kind),
		Name:            file.Name,
		Path:            file.Path,
		SizeBytes:       file.SizeBytes,
		ModifiedAt:      file.ModifiedAt,
		AlwaysLoaded:    file.AlwaysLoaded,
		EstimatedTokens: int64(file.EstimatedTokens),
	}
}
