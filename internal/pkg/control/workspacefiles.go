package control

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/config"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/workspacefs"
)

// ListWorkspaceFiles returns path metadata and direct directory entries.
func (r *Registry) ListWorkspaceFiles(
	ctx context.Context,
	sessionID uuid.UUID,
	requestedPath string,
) (WorkspaceFileListing, error) {
	cleanPath, err := validateWorkspaceFilePath(requestedPath, true)
	if err != nil {
		return WorkspaceFileListing{}, err
	}

	return withWorkspaceFileRoot(ctx, r, sessionID, func(
		root workspacefs.Root,
	) (WorkspaceFileListing, error) {
		return listWorkspaceFiles(ctx, root, requestedPath, cleanPath)
	})
}

func listWorkspaceFiles(
	ctx context.Context,
	root workspacefs.Root,
	requestedPath string,
	cleanPath string,
) (WorkspaceFileListing, error) {
	if err := ctx.Err(); err != nil {
		return WorkspaceFileListing{}, ctxerrors.Wrap(err,
			"list workspace files")
	}

	info, err := inspectWorkspacePath(root, cleanPath, false)
	if err != nil {
		return WorkspaceFileListing{}, workspaceFileError(err,
			"inspect workspace path")
	}

	listing := WorkspaceFileListing{
		Path:       requestedPath,
		Type:       fileType(info.Mode()),
		Size:       info.Size(),
		ModifiedAt: info.ModTime().UTC(),
		Entries:    make([]WorkspaceFileEntry, 0),
	}
	if !info.IsDir() {
		return listing, nil
	}

	entries, err := listWorkspaceEntries(ctx, root, cleanPath)
	if err != nil {
		return WorkspaceFileListing{}, err
	}

	listing.Entries = entries

	return listing, nil
}

func listWorkspaceEntries(
	ctx context.Context,
	root workspacefs.Root,
	cleanPath string,
) ([]WorkspaceFileEntry, error) {
	dirEntries, err := root.ReadDir(cleanPath)
	if err != nil {
		return nil, workspaceFileError(err, "read workspace directory")
	}

	entries := make([]WorkspaceFileEntry, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		if err := ctx.Err(); err != nil {
			return nil, ctxerrors.Wrap(err, "list workspace directory")
		}

		entryPath := path.Join(cleanPath, dirEntry.Name())

		// The directory itself already resolved inside the workspace, so the
		// entry's own metadata is enough. A symbolic link is reported as one
		// and never followed, wherever it points.
		info, err := root.Lstat(entryPath)
		if err != nil {
			return nil, workspaceFileError(err, "inspect workspace entry")
		}

		entries = append(entries, WorkspaceFileEntry{
			Name:       dirEntry.Name(),
			Type:       fileType(info.Mode()),
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC(),
		})
	}

	return entries, nil
}

// ReadWorkspaceFile returns bounded file bytes encoded for JSON transport.
func (r *Registry) ReadWorkspaceFile(
	ctx context.Context,
	sessionID uuid.UUID,
	requestedPath string,
) (WorkspaceFileContent, error) {
	cleanPath, err := validateWorkspaceFilePath(requestedPath, false)
	if err != nil {
		return WorkspaceFileContent{}, err
	}

	return withWorkspaceFileRoot(ctx, r, sessionID, func(
		root workspacefs.Root,
	) (WorkspaceFileContent, error) {
		return readWorkspaceFile(ctx, root, requestedPath, cleanPath,
			r.workspaceFileLimit())
	})
}

func readWorkspaceFile(
	ctx context.Context,
	root workspacefs.Root,
	requestedPath string,
	cleanPath string,
	maximum int64,
) (WorkspaceFileContent, error) {
	if err := ctx.Err(); err != nil {
		return WorkspaceFileContent{}, ctxerrors.Wrap(err,
			"read workspace file")
	}

	resolvedPath, err := root.RealPath(cleanPath)
	if err != nil {
		return WorkspaceFileContent{}, workspaceFileError(err,
			"resolve workspace file")
	}

	info, err := root.Stat(resolvedPath)
	if err != nil {
		return WorkspaceFileContent{}, workspaceFileError(err,
			"inspect workspace file")
	}

	if !info.Mode().IsRegular() {
		return WorkspaceFileContent{}, workspaceFileNotRegular()
	}

	if info.Size() > maximum {
		return WorkspaceFileContent{}, workspaceFileTooLarge()
	}

	data, err := readWorkspaceFileBytes(ctx, root, resolvedPath, maximum)
	if err != nil {
		return WorkspaceFileContent{}, err
	}

	return WorkspaceFileContent{
		Path:          requestedPath,
		Size:          int64(len(data)),
		ContentBase64: base64.StdEncoding.EncodeToString(data),
	}, nil
}

func readWorkspaceFileBytes(
	ctx context.Context,
	root workspacefs.Root,
	resolvedPath string,
	maximum int64,
) ([]byte, error) {
	file, err := root.Handle.OpenFile(
		resolvedPath,
		os.O_RDONLY|syscall.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, workspaceFileError(err, "open workspace file")
	}

	data, readErr := readOpenedWorkspaceFile(ctx, file, maximum)

	closeErr := file.Close()
	if closeErr != nil {
		closeErr = ctxerrors.Wrap(closeErr, "close workspace file")
	}

	return data, errors.Join(readErr, closeErr)
}

func readOpenedWorkspaceFile(
	ctx context.Context,
	file *os.File,
	maximum int64,
) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "inspect opened workspace file")
	}

	if !info.Mode().IsRegular() {
		return nil, workspaceFileNotRegular()
	}

	if info.Size() > maximum {
		return nil, workspaceFileTooLarge()
	}

	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read workspace file")
	}

	if int64(len(data)) > maximum {
		return nil, workspaceFileTooLarge()
	}

	if err := ctx.Err(); err != nil {
		return nil, ctxerrors.Wrap(err, "read workspace file")
	}

	return data, nil
}

// WriteWorkspaceFile atomically replaces one regular file with decoded bytes.
func (r *Registry) WriteWorkspaceFile(
	ctx context.Context,
	sessionID uuid.UUID,
	requestedPath string,
	contentBase64 string,
	createDirectories bool,
) (WorkspaceFileEntry, error) {
	cleanPath, err := validateWorkspaceFilePath(requestedPath, false)
	if err != nil {
		return WorkspaceFileEntry{}, err
	}

	data, err := decodeWorkspaceFile(contentBase64, r.workspaceFileLimit())
	if err != nil {
		return WorkspaceFileEntry{}, err
	}

	return withWorkspaceFileRoot(ctx, r, sessionID, func(
		root workspacefs.Root,
	) (WorkspaceFileEntry, error) {
		return writeWorkspaceFile(ctx, root, cleanPath, data,
			createDirectories)
	})
}

func decodeWorkspaceFile(contentBase64 string, maximum int64) ([]byte, error) {
	maxEncodedLength := base64.StdEncoding.EncodedLen(int(maximum))
	if int64(len(contentBase64)) > int64(maxEncodedLength) {
		return nil, workspaceFileTooLarge()
	}

	data, err := base64.StdEncoding.Strict().DecodeString(contentBase64)
	if err != nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"workspace file content must be valid base64",
		)
	}

	if int64(len(data)) > maximum {
		return nil, workspaceFileTooLarge()
	}

	return data, nil
}

func writeWorkspaceFile(
	ctx context.Context,
	root workspacefs.Root,
	cleanPath string,
	data []byte,
	createDirectories bool,
) (WorkspaceFileEntry, error) {
	if err := ctx.Err(); err != nil {
		return WorkspaceFileEntry{}, ctxerrors.Wrap(err,
			"write workspace file")
	}

	writePath, err := validateExistingWritePath(root, cleanPath)
	if err != nil {
		return WorkspaceFileEntry{}, err
	}

	if err := createWorkspaceParents(
		root,
		writePath,
		createDirectories,
	); err != nil {
		return WorkspaceFileEntry{}, err
	}

	if err := root.WriteFileAtomic(
		writePath, data, workspaceFileMode,
	); err != nil {
		return WorkspaceFileEntry{}, workspaceFileError(err,
			"write workspace file")
	}

	info, err := root.Lstat(writePath)
	if err != nil {
		return WorkspaceFileEntry{}, workspaceFileError(err,
			"inspect written workspace file")
	}

	return WorkspaceFileEntry{
		Name:       path.Base(cleanPath),
		Type:       fileType(info.Mode()),
		Size:       info.Size(),
		ModifiedAt: info.ModTime().UTC(),
	}, nil
}

func validateExistingWritePath(
	root workspacefs.Root,
	cleanPath string,
) (string, error) {
	existingPath, err := root.RealPath(cleanPath)
	if errors.Is(err, fs.ErrNotExist) {
		return cleanPath, nil
	}

	if err != nil {
		return "", workspaceFileError(err, "resolve existing workspace file")
	}

	info, err := root.Stat(existingPath)
	if err != nil {
		return "", workspaceFileError(err, "inspect existing workspace file")
	}

	if !info.Mode().IsRegular() {
		return "", workspaceFileNotRegular()
	}

	return existingPath, nil
}

func createWorkspaceParents(
	root workspacefs.Root,
	cleanPath string,
	createDirectories bool,
) error {
	parent := path.Dir(cleanPath)
	if !createDirectories || parent == "." {
		return nil
	}

	if err := root.MkdirAll(parent, workspaceDirectoryMode); err != nil {
		return workspaceFileError(err, "create workspace directories")
	}

	return nil
}

// DeleteWorkspaceFile removes a file, symlink, or empty directory.
func (r *Registry) DeleteWorkspaceFile(
	ctx context.Context,
	sessionID uuid.UUID,
	requestedPath string,
) error {
	cleanPath, err := validateWorkspaceFilePath(requestedPath, false)
	if err != nil {
		return err
	}

	_, err = withWorkspaceFileRoot(ctx, r, sessionID, func(
		root workspacefs.Root,
	) (struct{}, error) {
		return struct{}{}, deleteWorkspaceFile(ctx, root, cleanPath)
	})

	return err
}

func deleteWorkspaceFile(
	ctx context.Context,
	root workspacefs.Root,
	cleanPath string,
) error {
	if err := ctx.Err(); err != nil {
		return ctxerrors.Wrap(err, "delete workspace path")
	}

	info, err := inspectWorkspacePath(root, cleanPath, true)
	if err != nil {
		return workspaceFileError(err, "inspect workspace path")
	}

	if info.IsDir() {
		entries, err := root.ReadDir(cleanPath)
		if err != nil {
			return workspaceFileError(err, "inspect workspace directory")
		}

		if len(entries) != 0 {
			return ctxerrors.Wrap(commerr.ErrInvalidState,
				"workspace directory is not empty")
		}
	}

	if err := root.Remove(cleanPath); err != nil {
		return workspaceFileError(err, "delete workspace path")
	}

	return nil
}

//nolint:ireturn // Preserve each operation's concrete generated response type.
func withWorkspaceFileRoot[T any](
	ctx context.Context,
	r *Registry,
	sessionID uuid.UUID,
	operation func(workspacefs.Root) (T, error),
) (T, error) {
	root, err := openWorkspaceFileRoot(ctx, r, sessionID)
	if err != nil {
		var zero T

		return zero, err
	}

	result, operationErr := operation(root)

	closeErr := root.Handle.Close()
	if closeErr != nil {
		closeErr = ctxerrors.Wrap(closeErr, "close workspace root")
	}

	return result, errors.Join(operationErr, closeErr)
}

func openWorkspaceFileRoot(
	ctx context.Context,
	r *Registry,
	sessionID uuid.UUID,
) (workspacefs.Root, error) {
	if err := ctx.Err(); err != nil {
		return workspacefs.Root{}, ctxerrors.Wrap(err,
			"resolve workspace file session")
	}

	stored, err := r.workspaceSession(ctx, sessionID)
	if err != nil {
		return workspacefs.Root{}, err
	}

	handle, err := os.OpenRoot(stored.Workspace)
	if err != nil {
		return workspacefs.Root{}, workspaceFileError(err,
			"open stored workspace")
	}

	return workspacefs.Root{Handle: handle}, nil
}

func (r *Registry) workspaceFileLimit() int64 {
	if r.maxFileAPIBytes > 0 {
		return r.maxFileAPIBytes
	}

	return config.DefaultMaxFileAPIBytes
}

func validateWorkspaceFilePath(value string, allowRoot bool) (string, error) {
	if value == "" && allowRoot {
		return ".", nil
	}

	if value == "" || strings.ContainsRune(value, '\x00') ||
		path.IsAbs(value) || path.Clean(value) != value ||
		!fs.ValidPath(value) || value == "." {
		return "", ctxerrors.Wrap(workspacePathRefused(),
			"validate workspace path")
	}

	return value, nil
}

func inspectWorkspacePath(
	root workspacefs.Root,
	cleanPath string,
	allowDanglingSymlink bool,
) (fs.FileInfo, error) {
	_, resolveErr := root.RealPath(cleanPath)
	if resolveErr != nil &&
		(!allowDanglingSymlink || !errors.Is(resolveErr, fs.ErrNotExist)) {
		return nil, ctxerrors.Wrap(resolveErr,
			"resolve workspace metadata path")
	}

	linkInfo, err := root.Lstat(cleanPath)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "inspect workspace metadata path")
	}

	if resolveErr != nil && linkInfo.Mode()&fs.ModeSymlink == 0 {
		return nil, ctxerrors.Wrap(resolveErr,
			"resolve workspace metadata target")
	}

	return linkInfo, nil
}

func fileType(mode fs.FileMode) api.WorkspaceFileType {
	if mode&fs.ModeSymlink != 0 {
		return api.WorkspaceFileTypeSymlink
	}

	if mode.IsDir() {
		return api.WorkspaceFileTypeDirectory
	}

	return api.WorkspaceFileTypeFile
}

func workspacePathRefused() error {
	return errors.Join(commerr.ErrPermissionDenied, ErrWorkspacePathRefused)
}

func workspaceFileNotRegular() error {
	return ctxerrors.Wrap(commerr.ErrValidationFailed,
		"workspace path is not a regular file")
}

func workspaceFileTooLarge() error {
	return ctxerrors.Wrap(commerr.ErrValidationFailed,
		"workspace file exceeds the configured size limit")
}

func workspaceFileError(err error, operation string) error {
	switch {
	// A path that runs through a regular file, such as README.md/x, names
	// nothing that exists.
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return ctxerrors.Wrap(commerr.ErrNotFound, operation)
	// The operating system refused access to a path inside the workspace.
	// The path itself is fine, so this is not a path refusal.
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		return ctxerrors.Wrap(commerr.ErrPermissionDenied, operation)
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EXDEV),
		errors.Is(err, fs.ErrInvalid):
		return ctxerrors.Wrap(workspacePathRefused(), operation)
	case errors.Is(err, syscall.ENOTEMPTY), errors.Is(err, syscall.EEXIST):
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"workspace directory is not empty")
	default:
		return ctxerrors.Wrap(err, operation)
	}
}
