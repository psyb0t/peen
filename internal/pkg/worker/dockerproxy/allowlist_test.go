package dockerproxy

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassify(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		path string
		want access
	}{
		{path: "/_ping", want: accessForward},
		{path: "/v1.43/_ping", want: accessForward},
		{path: "/v10.100/info", want: accessForward},
		{path: "/events", want: accessForward},
		{path: "/system/df", want: accessForward},
		{path: "/containers/json", want: accessForward},
		{path: "/containers/abc/stats", want: accessForward},
		{path: "/containers/abc/json", want: accessRedactEnv},
		{path: "/containers/json/json", want: accessRedactEnv},
		{path: "/images/json", want: accessForward},
		{path: "/images/alpine/json", want: accessRedactEnv},
		{path: "/images/ghcr.io/org/app:1/json", want: accessRedactEnv},
		{path: "/networks/host", want: accessForward},
		{path: "/volumes/cache", want: accessForward},
		{path: "/", want: accessRefused},
		{path: "/v1.43", want: accessRefused},
		{path: "/v1.43/", want: accessRefused},
		{path: "/v1/info", want: accessRefused},
		{path: "/v.43/info", want: accessRefused},
		{path: "/v1./info", want: accessRefused},
		{path: "/V1.43/info", want: accessRefused},
		{path: "/v1.43/v1.43/info", want: accessRefused},
		{path: "/containers", want: accessRefused},
		{path: "/containers/abc", want: accessRefused},
		{path: "/containers/abc/logs", want: accessRefused},
		{path: "/containers/a/b/json", want: accessRefused},
		{path: "/images", want: accessRefused},
		{path: "/images/alpine", want: accessRefused},
		{path: "/images/alpine/history", want: accessRefused},
		{path: "/images/alpine/get", want: accessRefused},
		{path: "/images/json/get", want: accessRefused},
		{path: "/system/events", want: accessRefused},
		{path: "/networks/a/b", want: accessRefused},
		{path: "/volumes/a/b", want: accessRefused},
		{path: "/swarm", want: accessRefused},
		{path: "/plugins/x/json", want: accessRefused},
		{path: "/containers//json", want: accessRefused},
	}

	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, classify(tc.path))
		})
	}
}

func TestRequestsUpgrade(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		header http.Header
		want   bool
	}{
		{name: "no headers", header: http.Header{}},
		{
			name:   "keep alive",
			header: http.Header{"Connection": {"keep-alive"}},
		},
		{
			name:   "upgrade header",
			header: http.Header{"Upgrade": {"h2c"}},
			want:   true,
		},
		{
			name:   "connection upgrade in a list",
			header: http.Header{"Connection": {"close, UPGRADE"}},
			want:   true,
		},
		{
			name: "connection upgrade in a second value",
			header: http.Header{
				"Connection": {"keep-alive", "upgrade"},
			},
			want: true,
		},
		{
			name:   "a token that only contains upgrade",
			header: http.Header{"Connection": {"upgraded"}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, requestsUpgrade(tc.header))
		})
	}
}
