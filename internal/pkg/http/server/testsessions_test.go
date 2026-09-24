package server

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// testSessionRegistry is the control-surface double the handler tests wire in.
// Its zero value refuses every open, so a test that cares about opening a
// workspace sets the field it needs rather than inheriting a permissive stub.
type testSessionRegistry struct {
	opened   api.OpenedSession
	openErr  error
	page     api.SessionPage
	listErr  error
	requests []string

	// profiles records the profile name each open carried, so a routing test
	// can assert the client's choice reached the registry.
	profiles []string

	profileList api.ExecutionProfileList
	rootList    api.WorkspaceRootList
	workerPage  api.WorkerGenerationPage
	workersErr  error

	decision        api.SessionProfileDecision
	decisionErr     error
	decisionPage    api.SessionProfileDecisionPage
	decisionListErr error

	// reconfigured records every profile change the handler asked for, so a
	// test can assert the request body reached the registry unchanged.
	reconfigured []api.ReconfigureSessionRequest
}

func (r *testSessionRegistry) OpenWorkspaceSession(
	_ context.Context,
	workspace string,
	profile string,
) (api.OpenedSession, error) {
	r.requests = append(r.requests, workspace)
	r.profiles = append(r.profiles, profile)

	if r.openErr != nil {
		return api.OpenedSession{}, r.openErr
	}

	return r.opened, nil
}

func (r *testSessionRegistry) ExecutionProfiles() api.ExecutionProfileList {
	return r.profileList
}

func (r *testSessionRegistry) WorkspaceRoots() api.WorkspaceRootList {
	return r.rootList
}

func (r *testSessionRegistry) ListWorkerGenerations(
	_ context.Context,
	_ uuid.UUID,
	_ api.ListSessionWorkersParams,
) (api.WorkerGenerationPage, error) {
	if r.workersErr != nil {
		return api.WorkerGenerationPage{}, r.workersErr
	}

	return r.workerPage, nil
}

func (r *testSessionRegistry) ReconfigureSession(
	_ context.Context,
	_ uuid.UUID,
	request api.ReconfigureSessionRequest,
) (api.SessionProfileDecision, error) {
	r.reconfigured = append(r.reconfigured, request)

	if r.decisionErr != nil {
		return api.SessionProfileDecision{}, r.decisionErr
	}

	return r.decision, nil
}

func (r *testSessionRegistry) ListProfileDecisions(
	_ context.Context,
	_ uuid.UUID,
	_ api.ListSessionProfileDecisionsParams,
) (api.SessionProfileDecisionPage, error) {
	if r.decisionListErr != nil {
		return api.SessionProfileDecisionPage{}, r.decisionListErr
	}

	return r.decisionPage, nil
}

func (r *testSessionRegistry) ListSessions(
	_ context.Context,
	_ api.ListSessionsParams,
) (api.SessionPage, error) {
	if r.listErr != nil {
		return api.SessionPage{}, r.listErr
	}

	return r.page, nil
}

// testTurnRouter stands in for the control plane's worker routing.
//
// Production routes a turn to the session's worker over the private protocol.
// A transport test cares only that the routed session and request reached the
// router, so this records them and answers from the test runtime.
type testTurnRouter struct {
	runtime *testRuntime
	runErr  error

	// sessions records the session each routed turn named, so a routing test
	// can assert the transport chose it rather than the message body.
	sessions []uuid.UUID

	// cancelledSessions records the sessions whose worker was asked to stop, so
	// a test can prove the cancel endpoint reached the worker rather than only
	// writing the durable flag.
	cancelledSessions []uuid.UUID

	// cancelRouted is what the worker reports back for a cancel.
	cancelRouted bool

	// cancelErr fails the routed cancel.
	cancelErr error

	// signalledJobs records the jobs whose worker was asked to signal them, so
	// a test can prove the endpoint reached the worker holding the process
	// group rather than only writing the durable request.
	signalledJobs []uuid.UUID

	// signalSignal is the signal name the worker was asked for.
	signalSignal string

	// signalResponse is what the worker reports. Nil stands for a session with
	// no live worker.
	signalResponse *api.JobSignalResponse

	// signalErr fails the routed signal.
	signalErr error
}

func (r *testTurnRouter) RunSessionMessage(
	ctx context.Context,
	sessionID uuid.UUID,
	request agent.MessageRequest,
	requestID uuid.UUID,
) (*agent.MessageRunResult, error) {
	r.sessions = append(r.sessions, sessionID)
	if r.runErr != nil {
		return nil, r.runErr
	}

	return r.runtime.RunMessage(ctx, request, requestID, nil)
}

func (r *testTurnRouter) SignalSessionJob(
	_ context.Context,
	_ uuid.UUID,
	jobID uuid.UUID,
	signal string,
) (*api.JobSignalResponse, error) {
	r.signalledJobs = append(r.signalledJobs, jobID)

	if r.signalErr != nil {
		return nil, r.signalErr
	}

	if r.signalResponse == nil {
		return nil, nil //nolint:nilnil // Stands for no live worker.
	}

	r.signalResponse.JobId = jobID
	r.signalSignal = signal

	return r.signalResponse, nil
}

func (r *testTurnRouter) CancelSessionTurn(
	_ context.Context,
	sessionID uuid.UUID,
) (bool, error) {
	r.cancelledSessions = append(r.cancelledSessions, sessionID)

	if r.cancelErr != nil {
		return false, r.cancelErr
	}

	return r.cancelRouted, nil
}

// newTestServer builds a server for a handler test, supplying the control
// registry and turn router when the test does not care about them. Production
// New keeps requiring the dependencies, so these defaults never reach a
// deployment.
func newTestServer(deps Dependencies) (*Server, error) {
	if deps.Sessions == nil {
		deps.Sessions = &testSessionRegistry{
			openErr: ctxerrors.Wrap(
				commerr.ErrPermissionDenied,
				"test registry refuses every workspace by default",
			),
		}
	}

	if deps.Turns == nil {
		runtime, isTestRuntime := deps.Runtime.(*testRuntime)
		if isTestRuntime {
			deps.Turns = &testTurnRouter{runtime: runtime}
		}
	}

	return New(deps)
}
