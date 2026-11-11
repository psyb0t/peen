// Command benchdriver runs a scripted scenario against Peen inside a
// networkless benchmark task container.
//
// The benchmark adapter copies it into the container next to the Peen binary
// when a harness task ships a scenario. It starts `peen run` as a child
// process, opens the scenario's sessions in the control agent space, and
// executes the steps: sending messages over the WebSocket, answering
// questions, posting session events, stopping turns, killing the worker or
// the controller at a chosen tool call, restarting the controller, running
// commands that change the workspace, and sampling memory. At the end it
// writes every session's durable feed as a frames file and stops the
// controller, so graders read the same records an exec trial leaves.
//
// The driver log records every step, kill, and restart with its time, so
// the audit can line them up with Peen's records. See docs/benchmarks.md.
package main

import (
	"context"
	"flag"
	"math/rand/v2" // nosemgrep // Seeded so scenarios replay, never secrets.
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/control"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

// options are the driver's flags.
type options struct {
	scenarioPath string
	outputDir    string
	binary       string
}

func main() {
	ctx := context.Background()
	logger := ctxscope.GetLogger(ctx)

	opts := options{}
	flag.StringVar(&opts.scenarioPath, "scenario", "", "scenario.json path")
	flag.StringVar(&opts.outputDir, "out", defaultOutputDir, "output dir")
	flag.StringVar(&opts.binary, "peen", defaultPeenBinary, "peen binary")
	flag.Parse()

	if err := run(ctx, opts); err != nil {
		logger.Error("scenario failed", "err", err)
		os.Exit(exitCodeFailed)
	}
}

func run(ctx context.Context, opts options) error {
	if opts.scenarioPath == "" {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "--scenario")
	}

	parsed, err := loadScenario(opts.scenarioPath)
	if err != nil {
		return err
	}

	if err := ensureOutputDir(opts.outputDir); err != nil {
		return err
	}

	record, err := openRecorder(filepath.Join(opts.outputDir, driverLogName))
	if err != nil {
		return err
	}

	defer func() {
		if err := record.close(); err != nil {
			ctxscope.GetLogger(ctx).Warn("driver log close", "err", err)
		}
	}()

	r, err := newRunner(ctx, opts, parsed, record)
	if err != nil {
		return err
	}

	return r.execute(ctx, parsed)
}

func newRunner(
	ctx context.Context,
	opts options,
	parsed scenario,
	record *recorder,
) (*runner, error) {
	listenAddress := os.Getenv(listenAddressEnv)
	token := os.Getenv(apiTokenEnv)

	client, err := controlclient.New(controlclient.Options{
		ListenAddress: listenAddress,
		Token:         token,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build the control client")
	}

	seed := parsed.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}

	record.event(ctx, "scenario.started", map[string]any{
		"seed":     seed,
		"sessions": parsed.Sessions,
	})

	return &runner{
		client: client,
		control: newController(
			opts.binary,
			filepath.Join(opts.outputDir, runtimeLogName),
			client,
			record,
		),
		record:     record,
		sessions:   map[string]uuid.UUID{},
		outputDir:  opts.outputDir,
		baseURL:    "http://" + control.DialableAddress(listenAddress),
		token:      token,
		httpClient: &http.Client{Timeout: requestTimeout},
		//nolint:gosec // A reproducible chaos seed, not a secret.
		random: rand.New(rand.NewPCG(seed, seed)),
	}, nil
}

// execute starts the controller, opens the sessions, runs the steps, and
// always writes the feeds and stops the controller afterwards.
func (r *runner) execute(ctx context.Context, parsed scenario) error {
	if err := r.control.start(ctx); err != nil {
		return err
	}

	defer func() {
		if err := r.control.stop(ctx); err != nil {
			r.record.event(ctx, "controller.stopFailed", map[string]any{
				fieldErr: err.Error(),
			})
		}
	}()

	if err := r.openSessions(ctx, parsed.Sessions); err != nil {
		return err
	}

	sampleCtx, stopSampling := context.WithCancel(ctx)
	defer stopSampling()

	if parsed.SampleMemorySeconds > 0 {
		interval := time.Duration(parsed.SampleMemorySeconds) * time.Second

		go func() {
			defer func() {
				logRecovered(ctx, recover())
			}()

			sampleMemory(
				sampleCtx,
				filepath.Join(r.outputDir, memoryLogName),
				interval,
				r.control.pid,
			)
		}()
	}

	r.runSteps(ctx, parsed.Steps)
	stopSampling()

	r.writeFeeds(ctx)
	r.record.event(ctx, "scenario.finished", nil)

	return nil
}

func (r *runner) openSessions(
	ctx context.Context,
	sessions map[string]string,
) error {
	agentSpace := agentSpaceControl

	for name, workspace := range sessions {
		opened, err := r.client.OpenSession(ctx, api.OpenSessionRequest{
			Workspace:  workspace,
			AgentSpace: &agentSpace,
		})
		if err != nil {
			return ctxerrors.Wrapf(err, "open session %s", name)
		}

		r.sessions[name] = opened.Session.Id

		r.record.event(ctx, "session.opened", map[string]any{
			"name":      name,
			"sessionId": opened.Session.Id.String(),
			"workspace": workspace,
		})
	}

	return nil
}

// writeFeeds dumps each session's feed. The main session's goes to
// frames.jsonl, where exec trials keep theirs.
func (r *runner) writeFeeds(ctx context.Context) {
	if !r.control.isRunning() {
		if err := r.control.start(ctx); err != nil {
			r.record.event(ctx, "feed.skipped", map[string]any{
				fieldErr: err.Error(),
			})

			return
		}
	}

	for name, sessionID := range r.sessions {
		fileName := framesFilePrefix + name + jsonlSuffix
		if name == defaultSessionName {
			fileName = framesFileName
		}

		path := filepath.Join(r.outputDir, fileName)

		written, err := dumpFeed(ctx, r.client, sessionID, path)

		detail := map[string]any{"session": name, "frames": written}
		if err != nil {
			detail[fieldErr] = err.Error()
		}

		r.record.event(ctx, "feed.written", detail)
	}
}
