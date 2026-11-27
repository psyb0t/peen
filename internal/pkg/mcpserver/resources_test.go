package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceCatalogue(t *testing.T) {
	t.Parallel()
	client := testClient(t, (&testBoundary{}).dependencies())
	resources, err := client.ListResources(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, resources.Resources, 1)
	assert.Equal(t, "peen://sessions", resources.Resources[0].URI)
	assert.Equal(t, "application/json", resources.Resources[0].MIMEType)
	assert.NotEmpty(t, resources.Resources[0].Description)
	templates, err := client.ListResourceTemplates(t.Context(), nil)
	require.NoError(t, err)
	uris := make([]string, 0, len(templates.ResourceTemplates))
	for _, template := range templates.ResourceTemplates {
		uris = append(uris, template.URITemplate)
		assert.NotEmpty(t, template.Name)
		assert.NotEmpty(t, template.Description)
	}
	assert.ElementsMatch(t, []string{
		"peen://session/{sessionId}",
		"peen://session/{sessionId}/messages",
		"peen://session/{sessionId}/files/{+path}",
	}, uris)
}

func TestSessionsResource(t *testing.T) {
	t.Parallel()
	hidden, visible := uuid.New(), uuid.New()
	limits := make(chan int32, 1)
	boundary := &testBoundary{
		guard: func(_ context.Context, id uuid.UUID) error {
			if id == hidden {
				return commerr.ErrPermissionDenied
			}

			return nil
		},
		sessions: func(_ context.Context, p api.ListSessionsParams) (
			api.SessionPage, error,
		) {
			limits <- *p.Limit

			return api.SessionPage{
				Items: []api.Session{{Id: hidden}, {Id: visible}},
				Limit: *p.Limit, HasMore: true,
			}, nil
		},
	}
	client := testClient(t, boundary.dependencies())
	contents := readText(t, client, "peen://sessions")
	assert.Equal(t, "application/json", contents.MIMEType)
	var index struct {
		Items []struct {
			ID  uuid.UUID `json:"id"`
			URI string    `json:"uri"`
		} `json:"items"`
		Limit   int32 `json:"limit"`
		HasMore bool  `json:"hasMore"`
	}
	require.NoError(t, json.Unmarshal([]byte(contents.Text), &index))
	require.Len(t, index.Items, 1)
	assert.Equal(t, visible, index.Items[0].ID)
	assert.Equal(t, "peen://session/"+visible.String(), index.Items[0].URI)
	assert.EqualValues(t, 200, index.Limit)
	assert.True(t, index.HasMore)
	assert.EqualValues(t, 200, <-limits)
}

func TestSessionResource(t *testing.T) {
	t.Parallel()
	client := testClient(t, (&testBoundary{}).dependencies())
	id := uuid.New()
	contents := readText(t, client, "peen://session/"+id.String())
	assert.Equal(t, "application/json", contents.MIMEType)
	var stored api.Session
	require.NoError(t, json.Unmarshal([]byte(contents.Text), &stored))
	assert.Equal(t, id, stored.Id)
	assert.Equal(t, "/workspace", stored.Workspace)
}

func TestMessagesResource(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		maximum int32
		limit   int32
	}{
		{name: "default", limit: 50},
		{name: "configured", maximum: 7, limit: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			params := make(chan api.ListMessagesParams, 1)
			boundary := &testBoundary{messages: func(
				_ context.Context, p api.ListMessagesParams,
			) (*api.MessagePage, error) {
				params <- p

				return &api.MessagePage{Items: []api.Message{
					{Content: "newest"}, {Content: "older"},
				}, Limit: *p.Limit}, nil
			}}
			deps := boundary.dependencies()
			deps.MaxResourceMessages = tc.maximum
			client := testClient(t, deps)
			id := uuid.New()
			contents := readText(t, client,
				"peen://session/"+id.String()+"/messages")
			var page api.MessagePage
			require.NoError(t, json.Unmarshal([]byte(contents.Text), &page))
			require.Len(t, page.Items, 2)
			assert.Equal(t, "older", page.Items[0].Content)
			assert.Equal(t, "newest", page.Items[1].Content)
			request := <-params
			assert.Equal(t, id, request.XSessionID)
			assert.Equal(t, tc.limit, *request.Limit)
			assert.Equal(t, api.ListMessagesParamsOrderDesc, *request.Order)
		})
	}
}

func TestFileResourcePaths(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		encoded string
		path    string
	}{
		{"top level", "notes.txt", "notes.txt"},
		{"nested", "src/pkg/main.go", "src/pkg/main.go"},
		{"encoded slash", "src%2Fmain.go", "src/main.go"},
		{"encoded space", "my%20notes.txt", "my notes.txt"},
		{"literal percent", "100%25.txt", "100%.txt"},
		{"dots in a name", "a..b/c.d", "a..b/c.d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			listed := make(chan string, 1)
			read := make(chan string, 1)
			boundary := &testBoundary{
				listing: func(_ context.Context, _ uuid.UUID, path string) (
					control.WorkspaceFileListing, error,
				) {
					listed <- path

					return control.WorkspaceFileListing{
						Path: path, Type: api.WorkspaceFileTypeFile,
					}, nil
				},
				read: func(_ context.Context, _ uuid.UUID, path string) (
					control.WorkspaceFileContent, error,
				) {
					read <- path

					return control.WorkspaceFileContent{
						Path: path, ContentBase64: "aGk=",
					}, nil
				},
			}
			client := testClient(t, boundary.dependencies())
			uri := "peen://session/" + uuid.NewString() + "/files/" +
				tc.encoded
			contents := readText(t, client, uri)
			assert.Equal(t, "text/plain", contents.MIMEType)
			assert.Equal(t, "hi", contents.Text)
			assert.Equal(t, tc.path, <-listed)
			assert.Equal(t, tc.path, <-read)
		})
	}
}

func TestFileResourceRefusesEscapingPaths(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		encoded string
	}{
		{"parent", "../etc/passwd"},
		{"inner parent", "src/../../etc/passwd"},
		{"encoded parent", "%2e%2e/etc/passwd"},
		{"encoded upper parent", "%2E%2E%2Fetc%2Fpasswd"},
		{"double encoded parent", "%252e%252e/etc/passwd"},
		{"double encoded slash", "..%252Fetc"},
		{"absolute", "/etc/passwd"},
		{"encoded absolute", "%2Fetc%2Fpasswd"},
		{"current directory", "."},
		{"empty element", "src//main.go"},
		{"trailing slash", "src/"},
		{"NUL", "a%00b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			boundary := &testBoundary{}
			client := testClient(t, boundary.dependencies())
			failure := readFailure(t, client,
				"peen://session/"+uuid.NewString()+"/files/"+tc.encoded)
			assert.EqualValues(t, jsonrpc.CodeInvalidParams, failure.Code)
			assert.Equal(t, failureDenied, failure.Message)
			assert.Zero(t, boundary.accesses.Load())
		})
	}
}

func TestFileResourceKinds(t *testing.T) {
	t.Parallel()
	binary := []byte{0x00, 0xff, 0x10}
	boundary := &testBoundary{
		listing: func(_ context.Context, _ uuid.UUID, path string) (
			control.WorkspaceFileListing, error,
		) {
			kind := api.WorkspaceFileTypeFile
			if path == "" || path == "src" {
				kind = api.WorkspaceFileTypeDirectory
			}

			return control.WorkspaceFileListing{
				Path:    path,
				Type:    kind,
				Entries: []control.WorkspaceFileEntry{{Name: "main.go"}},
			}, nil
		},
		read: func(_ context.Context, _ uuid.UUID, path string) (
			control.WorkspaceFileContent, error,
		) {
			return control.WorkspaceFileContent{
				Path:          path,
				ContentBase64: base64.StdEncoding.EncodeToString(binary),
			}, nil
		},
	}
	client := testClient(t, boundary.dependencies())
	base := "peen://session/" + uuid.NewString() + "/files/"
	for _, path := range []string{"", "src"} {
		contents := readText(t, client, base+path)
		assert.Equal(t, "application/json", contents.MIMEType)
		var listing control.WorkspaceFileListing
		require.NoError(t, json.Unmarshal([]byte(contents.Text), &listing))
		assert.Equal(t, path, listing.Path)
		assert.Equal(t, api.WorkspaceFileTypeDirectory, listing.Type)
		require.Len(t, listing.Entries, 1)
	}
	contents := readText(t, client, base+"image.bin")
	assert.Equal(t, "application/octet-stream", contents.MIMEType)
	assert.Empty(t, contents.Text)
	assert.Equal(t, binary, contents.Blob)
}

func TestResourceFailures(t *testing.T) {
	t.Parallel()
	private := "private internal path /private/worker"
	for _, tc := range []struct {
		name    string
		uri     string
		guard   error
		listing error
		code    int64
		message string
	}{
		{
			name:    "unknown session",
			uri:     "peen://session/{id}",
			guard:   ctxerrors.Wrap(commerr.ErrNotFound, private),
			code:    jsonrpc.CodeInvalidParams,
			message: "Resource not found",
		},
		{
			name:    "denied session",
			uri:     "peen://session/{id}/messages",
			guard:   ctxerrors.Wrap(commerr.ErrPermissionDenied, private),
			code:    jsonrpc.CodeInvalidParams,
			message: failureDenied,
		},
		{
			name:    "malformed session",
			uri:     "peen://session/not-a-uuid",
			code:    jsonrpc.CodeInvalidParams,
			message: failureResource,
		},
		{
			name:    "missing file",
			uri:     "peen://session/{id}/files/gone",
			listing: ctxerrors.Wrap(commerr.ErrNotFound, private),
			code:    jsonrpc.CodeInvalidParams,
			message: "Resource not found",
		},
		{
			name:    "file too large",
			uri:     "peen://session/{id}/files/big",
			listing: ctxerrors.Wrap(commerr.ErrValidationFailed, private),
			code:    jsonrpc.CodeInvalidParams,
			message: failureResource,
		},
		{
			name:    "internal",
			uri:     "peen://session/{id}/files/broken",
			listing: ctxerrors.New(private),
			code:    jsonrpc.CodeInternalError,
			message: failureInternal,
		},
		{
			name:    "unregistered",
			uri:     "peen://elsewhere",
			code:    jsonrpc.CodeInvalidParams,
			message: "Resource not found",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			boundary := &testBoundary{
				guard: func(context.Context, uuid.UUID) error {
					return tc.guard
				},
				listing: func(context.Context, uuid.UUID, string) (
					control.WorkspaceFileListing, error,
				) {
					return control.WorkspaceFileListing{}, tc.listing
				},
			}
			client := testClient(t, boundary.dependencies())
			uri := strings.Replace(tc.uri, "{id}", uuid.NewString(), 1)
			failure := readFailure(t, client, uri)
			assert.Equal(t, tc.code, failure.Code)
			assert.Equal(t, tc.message, failure.Message)
			assert.NotContains(t, string(failure.Data), "private")
		})
	}
}
