//go:build integration

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/pkg/http/api/client"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const clientTestMessage = "run this through the generated client"

// The published client is generated from the same document the server is, so
// this is what proves the two actually agree on the wire rather than merely
// compiling from a shared spec. A caller outside this repo has nothing else to
// use, so a drift here is a drift nobody in this repo would otherwise notice.
func TestPublicClientDrivesTheRealAPI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	t.Cleanup(cancel)

	generated := newPublicClient(t)

	sent, err := generated.SendMessageWithResponse(
		ctx,
		&client.SendMessageParams{},
		client.SendMessageJSONRequestBody{Message: clientTestMessage},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, sent.StatusCode())
	require.NotNil(t, sent.JSON200)
	assert.NotEmpty(t, sent.JSON200.Message)

	sessionID, err := uuid.Parse(sent.HTTPResponse.Header.Get(headerSessionID))
	require.NoError(t, err)

	// The same client must be able to read back what it just wrote, which
	// exercises a header parameter and a query-parameter operation too.
	listed, err := generated.ListMessagesWithResponse(
		ctx,
		&client.ListMessagesParams{XSessionID: sessionID},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode())
	require.NotNil(t, listed.JSON200)
	require.GreaterOrEqual(t, len(listed.JSON200.Items), 2)
	assert.Equal(t, clientTestMessage, listed.JSON200.Items[0].Content)

	details, err := generated.GetSessionWithResponse(
		ctx,
		&client.GetSessionParams{XSessionID: sessionID},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, details.StatusCode())
	require.NotNil(t, details.JSON200)
	assert.Equal(t, sessionID, details.JSON200.Id)
	assert.NotEmpty(t, details.JSON200.Model)
}

// A generated client with no credentials must be refused exactly as a
// hand-rolled request is. The bearer check is server-wide middleware and this
// proves the generated paths do not somehow sit outside it.
func TestPublicClientWithoutATokenIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	t.Cleanup(cancel)

	generated, err := client.NewClientWithResponses(
		integrationInfra.APIURL(apiBasePath),
		client.WithHTTPClient(integrationInfra.HTTPClient()),
	)
	require.NoError(t, err)

	sent, err := generated.SendMessageWithResponse(
		ctx,
		&client.SendMessageParams{},
		client.SendMessageJSONRequestBody{Message: clientTestMessage},
	)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, sent.StatusCode())
}

// newPublicClient builds the generated client against the running deployment,
// authenticated the way a real caller would be.
func newPublicClient(t *testing.T) *client.ClientWithResponses {
	t.Helper()

	generated, err := client.NewClientWithResponses(
		integrationInfra.APIURL(apiBasePath),
		client.WithHTTPClient(integrationInfra.HTTPClient()),
		client.WithRequestEditorFn(func(
			_ context.Context,
			request *http.Request,
		) error {
			request.Header.Set(
				headerAuthorization,
				bearerPrefix+testinfra.TestAPIToken,
			)

			return nil
		}),
	)
	require.NoError(t, err)

	return generated
}
