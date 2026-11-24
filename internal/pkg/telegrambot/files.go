package telegrambot

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/telegram"
)

func (e *Engine) chatWorkspace(chatID int64) (string, error) {
	canonical, err := e.options.Sessions.ResolveWorkspace(e.options.Workspace)
	if err != nil || canonical != e.options.Workspace {
		return "", ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"Telegram workspace changed")
	}

	root, err := os.OpenRoot(canonical)
	if err != nil {
		return "", ctxerrors.Wrap(err, "open Telegram workspace")
	}

	// The sign stays, so a group and a user with the same digits never share
	// one directory.
	name := chatDirectoryPrefix + strconv.FormatInt(chatID, 10)
	createErr := createChatDirectory(root, name)

	closeErr := root.Close()
	if closeErr != nil {
		closeErr = ctxerrors.Wrap(closeErr, "close Telegram workspace")
	}

	if err := errors.Join(createErr, closeErr); err != nil {
		return "", err
	}

	workspace := filepath.Join(canonical, name)

	resolved, err := e.options.Sessions.ResolveWorkspace(workspace)
	if err != nil || resolved != workspace {
		return "", ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"Telegram chat directory is not allowed")
	}

	return workspace, nil
}

func createChatDirectory(root *os.Root, name string) error {
	if err := root.Mkdir(name, directoryMode); err != nil &&
		!errors.Is(err, fs.ErrExist) {
		return ctxerrors.Wrap(err, "create Telegram chat directory")
	}

	info, err := root.Lstat(name)
	if err != nil {
		return ctxerrors.Wrap(err, "inspect Telegram chat directory")
	}

	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"Telegram chat directory must be a real directory")
	}

	return nil
}

type incomingFile struct {
	id    string
	name  string
	size  int64
	photo bool
}

func incomingFiles(message telegram.Message) []incomingFile {
	files := make([]incomingFile, 0, maximumAttachments)

	if message.Document != nil {
		document := message.Document
		files = append(files, incomingFile{
			id:   document.FileID,
			name: document.FileName, size: document.FileSize,
		})
	}

	if message.Voice != nil {
		voice := message.Voice
		files = append(files, incomingFile{
			id:   voice.FileID,
			name: "voice.ogg", size: voice.FileSize,
		})
	}

	if len(message.Photo) != 0 {
		photo := message.Photo[len(message.Photo)-1]
		files = append(files, incomingFile{
			id:   photo.FileID,
			name: "photo.jpg", size: photo.FileSize, photo: true,
		})
	}

	return files
}

func (e *Engine) attachUploads(
	ctx context.Context, sessionID uuid.UUID, message telegram.Message,
	request *agent.MessageRequest,
) error {
	for _, file := range incomingFiles(message) {
		if err := e.attachFile(ctx, sessionID, file, request); err != nil {
			return err
		}
	}

	return nil
}

func (e *Engine) attachFile(
	ctx context.Context, sessionID uuid.UUID, file incomingFile,
	request *agent.MessageRequest,
) error {
	limit := e.options.MaxFileBytes
	if file.photo {
		limit = min(limit, int64(e.options.MaxImageBytes))
		if len(request.Images) >= e.options.MaxImages {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"too many Telegram images")
		}
	}

	if file.size < 0 || file.size > limit {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"Telegram attachment is too large")
	}

	data, err := e.options.Client.Download(ctx, file.id, limit)
	if err != nil {
		return ctxerrors.Wrap(err, "download Telegram attachment")
	}

	if int64(len(data)) > limit {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"Telegram attachment exceeds its limit")
	}

	if file.photo {
		mediaType := http.DetectContentType(data)
		if !strings.HasPrefix(mediaType, "image/") {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"Telegram photo is not an image")
		}

		request.Images = append(request.Images, agent.MessageImage{
			MediaType: mediaType, Data: data,
		})
	}

	name := uuid.NewString() + "-" + safeFilename(file.name)

	target := path.Join(uploadsDirectory, name)
	if _, err := e.options.Sessions.WriteWorkspaceFile(ctx, sessionID,
		target, base64.StdEncoding.EncodeToString(data), true); err != nil {
		return ctxerrors.Wrap(err, "save Telegram attachment")
	}

	request.Message = strings.TrimSpace(
		request.Message + "\nSaved file: " + target,
	)

	return nil
}

func safeFilename(value string) string {
	value = path.Base(strings.ReplaceAll(value, "\\", "/"))

	safe := strings.Map(safeFilenameCharacter, value)

	if len(safe) > maximumNameBytes {
		safe = safe[:maximumNameBytes]
	}

	safe = strings.Trim(safe, "._-")
	if safe == "" {
		return "attachment"
	}

	return safe
}

func safeFilenameCharacter(character rune) rune {
	if (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9') ||
		character == '.' || character == '_' || character == '-' {
		return character
	}

	return '_'
}
