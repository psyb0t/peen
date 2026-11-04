package tools

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testImageSide = 4

func testPNG(t *testing.T) []byte {
	t.Helper()

	picture := image.NewRGBA(image.Rect(0, 0, testImageSide, testImageSide))
	picture.Set(1, 1, color.RGBA{R: 255, A: 255})

	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, picture))

	return encoded.Bytes()
}

func TestReadFile_ImageReturnsBytesAndMetadata(t *testing.T) {
	t.Parallel()

	data := testPNG(t)

	testCases := []struct {
		name string
		file string
	}{
		{name: "lowercase extension", file: "shot.png"},
		{name: "uppercase extension", file: "SHOT.PNG"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			path := filepath.Join(workspace, tc.file)
			require.NoError(t, os.WriteFile(path, data, 0o600))

			exec := newReadFileExecutor(t, workspace)

			output, err := exec.ReadFile(context.Background(), ReadFileInput{
				Path: tc.file,
			})
			require.NoError(t, err)
			assert.Equal(t, path, output.Path)
			assert.Equal(t, "image/png", output.MediaType)
			assert.Equal(t, len(data), output.SizeBytes)
			assert.Equal(t, data, output.Image)
			assert.Empty(t, output.Content)
			assert.Equal(t, hashBytes(data), output.SHA256)
			assert.True(t, exec.Observed(path))
		})
	}
}

func TestReadFile_ImageRefusals(t *testing.T) {
	t.Parallel()

	data := testPNG(t)

	testCases := []struct {
		name     string
		file     string
		content  []byte
		maxBytes int
		wantErr  error
	}{
		{
			name:    "text named as an image",
			file:    "notes.png",
			content: []byte("not an image\n"),
			wantErr: ErrImageContent,
		},
		{
			name:    "png named as jpeg",
			file:    "shot.jpg",
			content: data,
			wantErr: ErrImageContent,
		},
		{
			name:     "one byte over the image limit",
			file:     "shot.png",
			content:  data,
			maxBytes: len(data) - 1,
			wantErr:  ErrLimitExceeded,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			require.NoError(t, os.WriteFile(
				filepath.Join(workspace, tc.file), tc.content, 0o600,
			))

			exec, err := NewExecutor(Options{
				Workspace: workspace,
				Limits:    Limits{MaxImageBytes: tc.maxBytes},
			})
			require.NoError(t, err)

			_, err = exec.ReadFile(context.Background(), ReadFileInput{
				Path: tc.file,
			})
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestReadFile_ImageAtExactLimit(t *testing.T) {
	t.Parallel()

	data := testPNG(t)
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "shot.png"), data, 0o600,
	))

	exec, err := NewExecutor(Options{
		Workspace: workspace,
		Limits:    Limits{MaxImageBytes: len(data)},
	})
	require.NoError(t, err)

	output, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path: "shot.png",
	})
	require.NoError(t, err)
	assert.Equal(t, data, output.Image)
}
