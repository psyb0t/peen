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
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	// reasonUnstructuredOutput marks a relayed line the worker did not write
	// as a JSON log record.
	reasonUnstructuredOutput = "unstructured_worker_output"

	// reasonRecordTooLong marks a worker line longer than maxRecordBytes,
	// which is dropped.
	reasonRecordTooLong = "worker_log_record_too_long"
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

	reader := bufio.NewReaderSize(stream, maxRecordBytes)

	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// The rest of an oversized line is skipped and reading goes on.
			// Stopping here would leave the worker blocked on a full pipe.
			logger.Warn("worker log record dropped",
				"reason", reasonRecordTooLong,
				"max_bytes", maxRecordBytes)

			if err := discardLine(reader); err != nil {
				endRelay(ctx, err)

				return
			}

			continue
		}

		relayLine(ctx, bytes.TrimRight(line, "\r\n"))

		if err != nil {
			endRelay(ctx, err)

			return
		}
	}
}

// endRelay records why the stream stopped. A closed stream is the normal end
// of a worker, and a read error on a dying process is not itself a controller
// fault, so neither is escalated beyond debug.
func endRelay(ctx context.Context, err error) {
	if !errors.Is(err, io.EOF) {
		ctxscope.GetLogger(ctx).Debug("worker log relay ended", "err", err)
	}
}

// discardLine reads past the end of the current line.
func discardLine(reader *bufio.Reader) error {
	for {
		_, err := reader.ReadSlice('\n')
		if !errors.Is(err, bufio.ErrBufferFull) {
			return err //nolint:wrapcheck // The caller only ends on it.
		}
	}
}

// relayLine re-emits one worker line through the controller's logger.
func relayLine(ctx context.Context, line []byte) {
	if len(line) == 0 {
		return
	}

	logger := ctxscope.GetLogger(ctx)

	// Workers log JSON, so a line that is not a record is something else
	// writing to the stream: a race report, a panic, or a subprocess. It is
	// relayed as a warning so it cannot hide among info records.
	level, message, attrs, ok := decodeRecord(line)
	if !ok {
		logger.Warn(
			workerOutputMessage,
			fieldWorkerOutput, string(line),
			"reason", reasonUnstructuredOutput,
		)

		return
	}

	logger.LogAttrs(ctx, level, message, attrs...)
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
