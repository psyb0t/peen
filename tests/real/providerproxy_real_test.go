//go:build real

package realtest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type realHarnessPromptRecord struct {
	markers map[string]bool
	roles   map[string]int
}

type realHarnessProviderProxy struct {
	target  *url.URL
	client  *http.Client
	markers []string

	mutex   sync.Mutex
	prompts []realHarnessPromptRecord
	server  *httptest.Server
}

func newRealHarnessProviderProxy(
	t *testing.T,
	upstreamBaseURL, workspace string,
) *realHarnessProviderProxy {
	t.Helper()

	target, err := url.Parse(upstreamBaseURL)
	require.NoError(t, err)
	require.True(t, target.IsAbs())
	workspaceMarker, err := json.Marshal(workspace)
	require.NoError(t, err)

	proxy := &realHarnessProviderProxy{
		target: target,
		client: &http.Client{},
		markers: []string{
			realHarnessConfigRuleMarker,
			realHarnessWorkspaceRuleMarker,
			realHarnessProjectsRuleMarker,
			realHarnessCatalogRuleMarker,
			realHarnessServiceRuleMarker,
			realHarnessRootAgentMarker,
			realHarnessSkill,
			realHarnessReviewer,
			realHarnessSkillBodyMarker,
			realHarnessReviewerMarker,
			realHarnessPatchEvent,
			realHarnessWakeHandlerMarker,
			realHarnessTrustedRuntimeMarker,
			realHarnessOperatingSystemLead + runtime.GOOS,
			realHarnessArchitectureLead + runtime.GOARCH,
			realHarnessTimezoneLead + time.Now().Location().String(),
			string(workspaceMarker),
		},
	}
	proxy.server = httptest.NewServer(http.HandlerFunc(proxy.handle))

	return proxy
}

func (p *realHarnessProviderProxy) URL() string {
	return p.server.URL
}

func (p *realHarnessProviderProxy) Close() {
	p.server.Close()
}

func (p *realHarnessProviderProxy) handle(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, "read provider request", http.StatusBadRequest)

		return
	}
	if closeErr := request.Body.Close(); closeErr != nil {
		http.Error(writer, "close provider request", http.StatusBadRequest)

		return
	}

	p.recordPrompt(body)

	forwardURL := *p.target
	forwardURL.Path = path.Join(p.target.Path, request.URL.Path)
	forwardURL.RawPath = ""
	forwardURL.RawQuery = request.URL.RawQuery
	forward, err := http.NewRequestWithContext(
		request.Context(),
		request.Method,
		forwardURL.String(),
		bytes.NewReader(body),
	)
	if err != nil {
		http.Error(writer, "build provider request", http.StatusInternalServerError)

		return
	}
	forward.Header = request.Header.Clone()

	//nolint:gosec // The proxy forwards to the operator-configured provider.
	response, err := p.client.Do(forward)
	if err != nil {
		http.Error(writer, "forward provider request", http.StatusBadGateway)

		return
	}
	defer func() { _ = response.Body.Close() }()

	for key, values := range response.Header {
		writer.Header()[key] = append([]string(nil), values...)
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = io.Copy(writer, response.Body)
}

func (p *realHarnessProviderProxy) recordPrompt(body []byte) {
	payload := any(nil)
	if err := json.Unmarshal(body, &payload); err != nil || !realHarnessPromptPayload(payload) {
		return
	}

	fragments := realHarnessTextFragments(payload)
	record := realHarnessPromptRecord{
		markers: make(map[string]bool, len(p.markers)),
		roles:   realHarnessRoles(payload),
	}
	for _, marker := range p.markers {
		for _, fragment := range fragments {
			if strings.Contains(fragment, marker) {
				record.markers[marker] = true

				break
			}
		}
	}

	p.mutex.Lock()
	p.prompts = append(p.prompts, record)
	p.mutex.Unlock()
}

func realHarnessPromptPayload(payload any) bool {
	object, ok := payload.(map[string]any)
	if !ok {
		return false
	}

	_, hasMessages := object["messages"]
	_, hasSystem := object["system"]

	return hasMessages || hasSystem
}

func realHarnessTextFragments(value any) []string {
	fragments := make([]string, 0)
	var visit func(any)
	visit = func(item any) {
		switch found := item.(type) {
		case string:
			fragments = append(fragments, found)
		case []any:
			for _, child := range found {
				visit(child)
			}
		case map[string]any:
			for _, child := range found {
				visit(child)
			}
		}
	}
	visit(value)

	return fragments
}

func realHarnessRoles(payload any) map[string]int {
	roles := map[string]int{}
	object, ok := payload.(map[string]any)
	if !ok {
		return roles
	}

	messages, ok := object["messages"].([]any)
	if !ok {
		return roles
	}
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			continue
		}

		role, ok := message["role"].(string)
		if ok {
			roles[role]++
		}
	}

	return roles
}

func (p *realHarnessProviderProxy) promptRecords() []realHarnessPromptRecord {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return append([]realHarnessPromptRecord(nil), p.prompts...)
}

func assertRealHarnessProviderContext(
	t *testing.T,
	proxy *realHarnessProviderProxy,
	workspace string,
) {
	t.Helper()

	workspaceMarker, err := json.Marshal(workspace)
	require.NoError(t, err)
	records := proxy.promptRecords()
	require.NotEmpty(t, records)

	initial := records[0]
	for _, marker := range []string{
		realHarnessConfigRuleMarker,
		realHarnessWorkspaceRuleMarker,
		realHarnessProjectsRuleMarker,
		realHarnessCatalogRuleMarker,
		realHarnessServiceRuleMarker,
		realHarnessRootAgentMarker,
		realHarnessSkill,
		realHarnessReviewer,
		realHarnessTrustedRuntimeMarker,
		realHarnessOperatingSystemLead + runtime.GOOS,
		realHarnessArchitectureLead + runtime.GOARCH,
		realHarnessTimezoneLead + time.Now().Location().String(),
		string(workspaceMarker),
	} {
		assert.True(
			t,
			initial.markers[marker],
			"initial provider context marker %q, present in requests %v",
			marker,
			realHarnessMarkerRequestIndexes(records, marker),
		)
	}
	assert.False(t, initial.markers[realHarnessSkillBodyMarker])
	assert.False(t, initial.markers[realHarnessReviewerMarker])

	assert.True(t, realHarnessRecordContains(records[1:], realHarnessSkillBodyMarker))
	assert.True(t, realHarnessRecordContains(records[1:], realHarnessReviewerMarker))
	assert.True(t, realHarnessRecordContains(records[1:], realHarnessPatchEvent))
	assert.True(t, realHarnessRecordContains(records, realHarnessWakeHandlerMarker))
	assert.True(t, realHarnessRecordWithAll(
		records,
		[]string{realHarnessReviewerMarker, string(workspaceMarker)},
	))
}

func realHarnessRecordContains(records []realHarnessPromptRecord, marker string) bool {
	for _, record := range records {
		if record.markers[marker] {
			return true
		}
	}

	return false
}

func realHarnessMarkerRequestIndexes(
	records []realHarnessPromptRecord,
	marker string,
) []int {
	indexes := make([]int, 0)
	for index, record := range records {
		if record.markers[marker] {
			indexes = append(indexes, index)
		}
	}

	return indexes
}

func realHarnessRecordWithAll(records []realHarnessPromptRecord, markers []string) bool {
	for _, record := range records {
		all := true
		for _, marker := range markers {
			if !record.markers[marker] {
				all = false

				break
			}
		}
		if all {
			return true
		}
	}

	return false
}
