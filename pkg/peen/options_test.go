package peen_test

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/pkg/peen"
	"github.com/stretchr/testify/require"
)

type nilModelResolver struct{ model peen.ModelClient }

func (r *nilModelResolver) ResolveModel(string) (peen.ModelClient, error) {
	return r.model, nil
}

func TestNewRejectsInvalidTuningAndDependencies(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		change    func(*peen.Options)
		wantError error
	}{
		{name: "negative event buffer", change: func(o *peen.Options) { o.EventLimits.SubscriberBuffer = -1 }, wantError: commerr.ErrValidationFailed},
		{name: "negative ask timeout", change: func(o *peen.Options) { o.AskTimeout = -time.Second }, wantError: commerr.ErrValidationFailed},
		{name: "negative tool count", change: func(o *peen.Options) { o.ToolLimits.MaxEdits = -1 }},
		{name: "negative command timeout", change: func(o *peen.Options) {
			o.ToolLimits.CommandTimeout = -time.Minute
		}},
		{name: "negative child depth", change: func(o *peen.Options) { o.AgentLimits.MaxDepth = -1 }},
		{name: "negative harness bytes", change: func(o *peen.Options) { o.HarnessLimits.MaxFileBytes = -1 }},
		{name: "harness total below file", change: func(o *peen.Options) { o.HarnessLimits.MaxFileBytes = 1024; o.HarnessLimits.MaxTotalContextBytes = 512 }},
		{name: "reserved disable", change: func(o *peen.Options) { o.DisableTools = []string{"ask"} }, wantError: commerr.ErrValidationFailed},
		{name: "unknown disable", change: func(o *peen.Options) { o.DisableTools = []string{"misspelledTool"} }, wantError: commerr.ErrValidationFailed},
		{name: "duplicate disable", change: func(o *peen.Options) { o.DisableTools = []string{"readFile", "readFile"} }, wantError: commerr.ErrValidationFailed},
		{name: "nil filesystem pointer", change: func(o *peen.Options) { o.Filesystem = (*peen.RootedFS)(nil); o.WorkspaceID = "nil-files" }, wantError: commerr.ErrValidationFailed},
		{name: "nil resolver pointer", change: func(o *peen.Options) { o.ModelResolver = (*nilModelResolver)(nil) }, wantError: commerr.ErrValidationFailed},
		{name: "nil runner pointer", change: func(o *peen.Options) { o.CommandRunner = (*recordingRunner)(nil) }, wantError: commerr.ErrValidationFailed},
		{name: "nil metrics registry", change: func(o *peen.Options) { o.MetricsRegisterer = (*prometheus.Registry)(nil) }, wantError: commerr.ErrValidationFailed},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := validOptions(t)
			options.Workspace = t.TempDir()
			tc.change(&options)
			var runtime *peen.Runtime
			var err error
			require.NotPanics(t, func() { runtime, err = peen.New(options) })
			if runtime != nil {
				t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
			}
			require.Error(t, err)
			if tc.wantError != nil {
				require.ErrorIs(t, err, tc.wantError)
			}
		})
	}
}
