// Package workerlog carries a worker process's log records into the
// controller's own logging stack.
//
// A worker runs the model loop in its own process, so its records for hook
// actions, skill activation, child agents, and tool outcomes are written by
// that process. Left alone they reach only the worker's own output, which the
// controller's audit sink never sees, and the durable audit trail loses
// everything the agent did. Relaying them through the controller's logger puts
// them back in front of every configured sink.
package workerlog

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxscope"
)

const (
	// maxRecordBytes bounds one relayed line. A worker may print a long tool
	// result, and an unbounded scanner buffer would let it decide this
	// process's memory use.
	maxRecordBytes = 1 << 20

	fieldTime    = "time"
	fieldLevel   = "level"
	fieldMessage = "msg"
	fieldSource  = "source"

	// workerOutputMessage labels a line the worker did not write through slog,
	// such as output from a subprocess it started.
	workerOutputMessage = "worker output"

	fieldWorkerOutput = "output"
)

// Relay reads a worker's output stream and re-emits each record through the
// controller's logger.
//
// It returns when the stream ends, so callers run it on its own goroutine and
// close the stream to stop it. Every relayed record keeps the worker's own
// level and message and gains the session and generation that produced it, so
// an operator reading one audit file can tell which worker spoke.
func Relay(
	ctx context.Context,
	stream io.Reader,
	sessionID uuid.UUID,
	generationID uuid.UUID,
) {
	ctx = ctxscope.Set(
		ctx,
		ctxscope.Attr("session_id", sessionID.String()),
		ctxscope.Attr("worker_generation_id", generationID.String()),
	)
	logger := ctxscope.GetLogger(ctx)

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), maxRecordBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		level, message, attrs, ok := decodeRecord(line)
		if !ok {
			logger.Info(workerOutputMessage, fieldWorkerOutput, string(line))

			continue
		}

		logger.LogAttrs(ctx, level, message, attrs...)
	}

	// A closed stream is the normal end of a worker, and a read error on a
	// dying process is not itself a controller fault, so neither is escalated
	// beyond debug.
	if err := scanner.Err(); err != nil {
		logger.Debug("worker log relay ended", "err", err)
	}
}

// decodeRecord turns one JSON log line into the parts needed to re-emit it.
// It reports false for any line the worker did not write as a log record.
func decodeRecord(line []byte) (slog.Level, string, []slog.Attr, bool) {
	fields := map[string]any{}
	if err := json.Unmarshal(line, &fields); err != nil {
		return 0, "", nil, false
	}

	message, hasMessage := fields[fieldMessage].(string)
	if !hasMessage {
		return 0, "", nil, false
	}

	level := slog.LevelInfo
	if text, hasLevel := fields[fieldLevel].(string); hasLevel {
		// A level this process does not know stays at info rather than
		// dropping the record.
		_ = level.UnmarshalText([]byte(text))
	}

	attrs := make([]slog.Attr, 0, len(fields))

	for key, value := range fields {
		// The controller stamps its own time, and its own source location
		// would otherwise be overwritten by the worker's.
		if key == fieldMessage || key == fieldLevel ||
			key == fieldTime || key == fieldSource {
			continue
		}

		attrs = append(attrs, slog.Any(key, value))
	}

	return level, message, attrs, true
}
