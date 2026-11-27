package openaicompat

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func imageData(marker byte) []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', marker}
}

func imagePart(value string) contentPart {
	return contentPart{Type: partImage, ImageURL: &imageURL{URL: value}}
}

func imageDataURL(data []byte) string {
	return dataURLPrefix + mediaTypePNG + base64Suffix + "," +
		base64.StdEncoding.EncodeToString(data)
}

func TestFreshCompletionPreservesEarlierAndCurrentImages(t *testing.T) {
	t.Parallel()

	first, last := imageData('A'), imageData('B')
	body := encodedBody(t, map[string]any{"messages": []any{
		map[string]any{"role": roleUser, "content": []contentPart{
			{Type: partText, Text: "Before "}, imagePart(imageDataURL(first)),
		}},
		map[string]any{"role": roleAssistant, "content": "Earlier answer"},
		map[string]any{"role": roleUser, "content": []contentPart{
			{Type: partText, Text: "After "}, imagePart(imageDataURL(last)),
		}},
	}})
	h := testHandler(t, testDependencies(func(
		_ context.Context,
		request control.ConversationRequest,
	) (*control.ConversationResult, error) {
		require.NotNil(t, request.CreatedMessage)
		created := *request.CreatedMessage
		require.Len(t, created.Images, 2)
		assert.Equal(t, first, created.Images[0].Data)
		assert.Equal(t, last, created.Images[1].Data)
		assert.Equal(t, mediaTypePNG, created.Images[0].MediaType)
		assert.Contains(t, created.Message,
			`<message role="user">Before [Attached image 1]</message>`)
		assert.True(t, strings.HasSuffix(created.Message,
			"After [Attached image 2]"))
		require.Len(t, request.Message.Images, 1)
		assert.Equal(t, last, request.Message.Images[0].Data)
		assert.Equal(t, "After [Attached image 1]", request.Message.Message)

		return testResult(), nil
	}))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, postRequest(t.Context(), body))

	require.Equal(t, http.StatusOK, response.Code)
}

func TestCompletionRejectsRemoteAndMalformedImages(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"http://invalid.example/image.png", "https://invalid.example/image.png",
		"file:///private/image.png", "data:image/png,plain",
		"data:image/png;base64,%%%%", "data:image/png;base64,",
		"data:text/plain;base64,aGVsbG8=", "data:image/jpeg;base64," +
			base64.StdEncoding.EncodeToString(imageData('A')),
		imageDataURL(make([]byte, testMaxImageBytes+1)),
	} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			body := encodedBody(t, map[string]any{"messages": []any{
				map[string]any{"role": roleUser, "content": []contentPart{
					imagePart(value),
				}},
			}})
			assertRejected(t, body)
		})
	}
}

func TestCompletionImagesRespectRoleAndCombinedCount(t *testing.T) {
	t.Parallel()

	for _, role := range []string{roleAssistant, roleSystem} {
		body := encodedBody(t, map[string]any{"messages": []any{
			map[string]any{"role": role, "content": []contentPart{
				imagePart(imageDataURL(imageData('A'))),
			}},
			map[string]any{"role": roleUser, "content": "Question"},
		}})
		assertRejected(t, body)
	}

	parts := make([]contentPart, 0, testMaxImages+1)
	for range testMaxImages + 1 {
		parts = append(parts, imagePart(imageDataURL(imageData('A'))))
	}

	body := encodedBody(t, map[string]any{"messages": []any{
		map[string]any{"role": roleUser, "content": parts[:testMaxImages]},
		map[string]any{"role": roleUser, "content": parts[testMaxImages:]},
	}})
	assertRejected(t, body)
}
