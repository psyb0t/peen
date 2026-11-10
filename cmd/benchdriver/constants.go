package main

import "time"

const (
	defaultSessionName = "main"
	defaultWorkspace   = "/app"
	defaultOutputDir   = "/peen"
	defaultPeenBinary  = "/usr/local/bin/peen"
	agentSpaceControl  = "control"
	serveCommand       = "run"
	workerCommand      = "worker"
	iterationToken     = "{i}"

	runtimeLogName   = "runtime.log"
	driverLogName    = "driver.jsonl"
	memoryLogName    = "memory.jsonl"
	framesFileName   = "frames.jsonl"
	framesFilePrefix = "frames-"
	liveFilePrefix   = "live-"
	jsonlSuffix      = ".jsonl"

	listenAddressEnv = "PEEN_HTTP_LISTEN_ADDRESS"
	//nolint:gosec // The name of the variable, not a credential.
	apiTokenEnv = "PEEN_API_TOKEN"

	fieldPID   = "pid"
	fieldErr   = "err"
	fieldLabel = "label"

	frameToolUse       = "tool.use"
	frameQuestionAsked = "question.asked"
	frameTurnCompleted = "turn.completed"
	frameTurnFailed    = "turn.failed"
	frameTurnCancelled = "turn.cancelled"

	noticesPath       = "/api/v1/session/notices"
	runningAgentsPath = "/api/v1/session/agents?state=running&limit=1"
	headerSessionID   = "X-Session-ID"
	bearerScheme      = "Bearer "

	readyTimeout       = 2 * time.Minute
	readyPollInterval  = 250 * time.Millisecond
	idlePollInterval   = time.Second
	killFilePoll       = 100 * time.Millisecond
	defaultIdleTimeout = 30 * time.Minute
	defaultIdleSettle  = 3 * time.Second
	defaultTurnTimeout = 30 * time.Minute
	stopGrace          = 30 * time.Second
	requestTimeout     = 30 * time.Second
	reattachTimeout    = 30 * time.Minute

	outputFileMode   = 0o644
	outputDirMode    = 0o755
	procRoot         = "/proc"
	procStatusName   = "status"
	procStatName     = "stat"
	procCmdlineName  = "cmdline"
	residentPrefix   = "VmRSS:"
	kibibyte         = 1024
	statParentField  = 1
	cmdlineSeparator = "\x00"

	exitCodeFailed = 1
)
