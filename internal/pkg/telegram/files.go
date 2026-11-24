package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/psyb0t/aichteeteapee"
)

func (c *Client) GetFile(ctx context.Context, fileID string) (File, error) {
	if !validFileID(fileID) {
		return File{}, invalidInput()
	}

	payload, err := json.Marshal(fileRequest{FileID: fileID})
	if err != nil {
		return File{}, invalidInput()
	}

	result, err := c.call(ctx, methodGetFile, bytes.NewReader(payload),
		aichteeteapee.ContentTypeJSON, regularTimeout)
	if err != nil {
		return File{}, err
	}

	var file File

	if err := json.Unmarshal(result, &file); err != nil {
		return File{}, invalidResponse()
	}

	if !validFileID(file.FileID) || file.FileSize < 0 ||
		!validFilePath(file.FilePath) {
		return File{}, invalidResponse()
	}

	return file, nil
}

func (c *Client) Download(
	ctx context.Context, fileID string, maxBytes int64,
) ([]byte, error) {
	if maxBytes < 1 || maxBytes > maxDownloadBytes {
		return nil, invalidInput()
	}

	file, err := c.GetFile(ctx, fileID)
	if err != nil {
		return nil, err
	}

	if file.FileSize > maxBytes {
		return nil, sizeFailure()
	}

	ctx, cancel := context.WithTimeout(ctx, regularTimeout)
	defer cancel()

	return c.exchange(ctx, http.MethodGet,
		c.baseURL+"/file/bot"+c.token+"/"+file.FilePath,
		nil, "", maxBytes)
}
