package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
)

const (
	transcriptsDirectoryName = "transcripts"
	agentsDirectoryName      = "agents"
	transcriptFileExtension  = ".jsonl"

	transcriptDirPerm  = 0o700
	transcriptFilePerm = 0o600
)

// transcriptLine is one JSONL record for an agent run's mirror. It keeps the
// parent launch call ID on every line so a reader can correlate a run's own
// transcript back to the tool call that started it.
type transcriptLine struct {
	Sequence         int             `json:"sequence"`
	RunID            uuid.UUID       `json:"runId"`
	ParentToolCallID string          `json:"parentToolCallId,omitempty"`
	Type             string          `json:"type"`
	Payload          json.RawMessage `json:"payload"`
	CreatedAt        time.Time       `json:"createdAt"`
}

// agentRunTranscript appends one JSONL line per event for one agent run,
// mirroring the durable record alongside the session's own transcript for
// tailing and after-the-fact inspection. SQLite remains authoritative; a
// write failure here degrades only this mirror and is logged, never
// returned to a caller.
//
// An empty path (no configured directory, or an agent name that could not be
// made into a safe path segment) disables the mirror: Append becomes a
// silent no-op rather than ever writing outside the intended directory.
type agentRunTranscript struct {
	path string
}

// agentRunSessionDirectory returns the directory holding every one of a
// session's agent run transcript subdirectories, the shared base that both
// newAgentRunTranscript (a known agent name) and findAgentRunTranscriptPath
// (an unknown one) resolve a run's file under.
func agentRunSessionDirectory(
	configDirectory string,
	sessionID uuid.UUID,
) string {
	return filepath.Join(
		configDirectory,
		transcriptsDirectoryName,
		sessionID.String(),
		agentsDirectoryName,
	)
}

// newAgentRunTranscript resolves and sanitizes the on-disk path for one
// agent run's mirror:
//
//	<configDirectory>/transcripts/<sessionID>/agents/<agentName>/<runID>.jsonl
//
// configDirectory empty disables mirroring, matching a nil Events bus.
// agentName comes from resolved metadata or a caller-supplied ad-hoc
// definition, so it is untrusted for path building: a name that cannot be
// made safe also disables mirroring for this run. That is logged, never a
// write outside the session's directory.
func newAgentRunTranscript(
	ctx context.Context,
	configDirectory string,
	sessionID uuid.UUID,
	agentName string,
	runID uuid.UUID,
) *agentRunTranscript {
	if configDirectory == "" {
		return &agentRunTranscript{}
	}

	segment, ok := sanitizePathSegment(agentName)
	if !ok {
		ctxscope.GetLogger(ctx).Warn(
			"agent run transcript disabled: unsafe agent name",
			"session_id", sessionID,
			"run_id", runID,
		)

		return &agentRunTranscript{}
	}

	sessionDirectory := agentRunSessionDirectory(configDirectory, sessionID)

	path := filepath.Clean(filepath.Join(
		sessionDirectory,
		segment,
		runID.String()+transcriptFileExtension,
	))

	// Defense in depth beyond the character checks in sanitizePathSegment:
	// verify the resolved path still lives inside the session's own agents
	// directory, so no lexical trick can land a write outside it.
	if !pathContains(sessionDirectory, path) {
		ctxscope.GetLogger(ctx).Warn(
			"agent run transcript disabled: path escapes session directory",
			"session_id", sessionID,
			"run_id", runID,
		)

		return &agentRunTranscript{}
	}

	return &agentRunTranscript{path: path}
}

// Append writes one line for this run's mirror. A disabled mirror (empty
// path) is a silent no-op; any other failure is logged and swallowed, never
// returned, so a mirror problem never fails the turn.
func (t *agentRunTranscript) Append(ctx context.Context, line transcriptLine) {
	if t.path == "" {
		return
	}

	encoded, err := json.Marshal(line)
	if err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"encode agent run transcript line",
			"err", err,
			"run_id", line.RunID,
		)

		return
	}

	if err := appendJSONLine(ctx, t.path, encoded); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"write agent run transcript line",
			"err", err,
			"run_id", line.RunID,
		)
	}
}

// appendJSONLine appends one line, creating the file and its parent
// directory on first use.
func appendJSONLine(ctx context.Context, path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), transcriptDirPerm); err != nil {
		return ctxerrors.Wrap(err, "create transcript directory")
	}

	file, err := os.OpenFile(
		path,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		transcriptFilePerm,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "open transcript file")
	}

	defer func() {
		if cerr := file.Close(); cerr != nil {
			ctxscope.GetLogger(ctx).Warn(
				"close agent run transcript file",
				"err", cerr,
			)
		}
	}()

	if _, err := file.Write(append(line, '\n')); err != nil {
		return ctxerrors.Wrap(err, "write transcript line")
	}

	return nil
}

// sanitizePathSegment validates one untrusted path segment (an agent name)
// before it becomes a directory name. It rejects anything empty, ".", "..",
// an embedded NUL, an absolute path, or anything containing a real path
// separator. A Unicode character that merely LOOKS like a separator or a dot
// (a fullwidth solidus, for example) is not one: filepath.Join and the
// filesystem treat it as an ordinary character, so it is accepted here and
// still verified by the caller's final containment check.
func sanitizePathSegment(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed == "." || trimmed == ".." {
		return "", false
	}

	if strings.ContainsAny(trimmed, "/\\") {
		return "", false
	}

	if strings.ContainsRune(trimmed, 0) {
		return "", false
	}

	if filepath.IsAbs(trimmed) {
		return "", false
	}

	return trimmed, true
}

// pathContains reports whether path is root itself or lexically nested
// inside it. Both arguments must already be filepath.Clean'd absolute-or-
// relative paths built from the same base, which newAgentRunTranscript
// guarantees.
func pathContains(root, path string) bool {
	if path == root {
		return true
	}

	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// findAgentRunTranscriptPath locates one run's durable JSONL mirror by run
// ID alone, for a run whose in-memory registry entry is gone, most commonly
// a service restart. The run ID segment of the filename is a UUID, so a
// single-level glob under the session's agents directory finds it without
// ever building a path from an untrusted agent name; no sanitizePathSegment
// call is needed because no untrusted string becomes a path segment here. An
// empty configDirectory, a malformed glob pattern, or no match reports
// ok=false.
func findAgentRunTranscriptPath(
	configDirectory string,
	sessionID uuid.UUID,
	runID uuid.UUID,
) (string, bool) {
	if configDirectory == "" {
		return "", false
	}

	sessionDirectory := agentRunSessionDirectory(configDirectory, sessionID)

	matches, err := filepath.Glob(filepath.Join(
		sessionDirectory,
		"*",
		runID.String()+transcriptFileExtension,
	))
	if err != nil || len(matches) == 0 {
		return "", false
	}

	path := filepath.Clean(matches[0])

	// Defense in depth, matching newAgentRunTranscript's own containment
	// check: a match must still live inside the session's agents directory.
	if !pathContains(sessionDirectory, path) {
		return "", false
	}

	return path, true
}

// readAgentRunTranscript reads one run's durable JSONL mirror and returns
// the same (events, nextCursor) shape the in-memory ring buffer's ReadEvents
// returns, so a caller can follow a run across the boundary between "still
// buffered" and "durable only" without a special case. Nothing is ever
// evicted from the file, so there is no dropped count to report; a cursor
// past the last written event returns no events and echoes cursor back
// unchanged, matching the buffer's own behavior. A missing file reports
// commerr.ErrNotFound, the sentinel this package already uses for a missing
// agent run.
func readAgentRunTranscript(
	ctx context.Context,
	path string,
	cursor, maxEvents int,
) ([]AgentRunEvent, int, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, ctxerrors.Wrap(
			commerr.ErrNotFound, "agent run transcript",
		)
	}

	if err != nil {
		return nil, 0, ctxerrors.Wrap(err, "open agent run transcript")
	}

	defer func() {
		if cerr := file.Close(); cerr != nil {
			ctxscope.GetLogger(ctx).Warn(
				"close agent run transcript file",
				"err", cerr,
			)
		}
	}()

	events, err := decodeAgentRunTranscriptLines(file, cursor, maxEvents)
	if err != nil {
		return nil, 0, err
	}

	next := cursor
	if len(events) > 0 {
		next = events[len(events)-1].Sequence + 1
	}

	return events, next, nil
}

// decodeAgentRunTranscriptLines reads every line at or after cursor, up to
// maxEvents (maxEvents <= 0 means no cap, matching agentRunEventBuffer.Read).
// It uses a bufio.Reader rather than bufio.Scanner because one event's
// payload can exceed Scanner's fixed token limit.
func decodeAgentRunTranscriptLines(
	file *os.File,
	cursor, maxEvents int,
) ([]AgentRunEvent, error) {
	reader := bufio.NewReader(file)
	events := make([]AgentRunEvent, 0)

	for {
		raw, readErr := reader.ReadBytes('\n')

		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 {
			var line transcriptLine
			if err := json.Unmarshal(trimmed, &line); err != nil {
				return nil, ctxerrors.Wrap(
					err,
					"decode agent run transcript line",
				)
			}

			if line.Sequence >= cursor {
				events = append(events, AgentRunEvent{
					Sequence:  line.Sequence,
					Type:      line.Type,
					Payload:   line.Payload,
					CreatedAt: line.CreatedAt,
				})

				if maxEvents > 0 && len(events) >= maxEvents {
					break
				}
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}

			return nil, ctxerrors.Wrap(readErr, "read agent run transcript")
		}
	}

	return events, nil
}
