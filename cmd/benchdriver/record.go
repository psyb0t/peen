package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

// feedItem is one stored frame as the session feed endpoint returns it.
type feedItem = api.FeedFrame

// recorder writes the driver log: one JSON object per line for every step,
// kill, restart, and failure, so the audit can line them up with Peen's own
// records.
type recorder struct {
	mu      sync.Mutex
	encoder *json.Encoder
	closer  io.Closer
}

// driverEvent is one driver log line.
type driverEvent struct {
	Time   time.Time      `json:"time"`
	Event  string         `json:"event"`
	Detail map[string]any `json:"detail,omitempty"`
}

func openRecorder(path string) (*recorder, error) {
	file, err := os.OpenFile(
		path,
		os.O_WRONLY|os.O_CREATE|os.O_APPEND,
		outputFileMode,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open the driver log")
	}

	return &recorder{encoder: json.NewEncoder(file), closer: file}, nil
}

func (r *recorder) event(
	ctx context.Context,
	name string,
	detail map[string]any,
) {
	r.mu.Lock()
	defer r.mu.Unlock()

	err := r.encoder.Encode(driverEvent{
		Time:   time.Now().UTC(),
		Event:  name,
		Detail: detail,
	})
	if err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"driver log write failed",
			"event", name,
			"err", err,
		)
	}
}

func (r *recorder) close() error {
	if err := r.closer.Close(); err != nil {
		return ctxerrors.Wrap(err, "close the driver log")
	}

	return nil
}

// memorySample is one resident memory reading of the process tree.
type memorySample struct {
	Time       time.Time       `json:"time"`
	Controller int64           `json:"controllerBytes"`
	Workers    int64           `json:"workerBytes"`
	Total      int64           `json:"totalBytes"`
	Processes  []processMemory `json:"processes"`
}

type processMemory struct {
	PID      int    `json:"pid"`
	Command  string `json:"command"`
	Resident int64  `json:"residentBytes"`
}

// sampleMemory writes a memory sample at every interval until ctx ends.
func sampleMemory(
	ctx context.Context,
	path string,
	interval time.Duration,
	controllerPID func() int,
) {
	logger := ctxscope.GetLogger(ctx)

	file, err := os.Create(path)
	if err != nil {
		logger.Warn("memory log open failed", "err", err)

		return
	}

	defer func() {
		if err := file.Close(); err != nil {
			logger.Warn("memory log close failed", "err", err)
		}
	}()

	encoder := json.NewEncoder(file)
	ticker := time.NewTicker(interval)

	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		sample, err := takeSample(controllerPID())
		if err != nil {
			logger.Debug("memory sample skipped", "err", err)

			continue
		}

		if err := encoder.Encode(sample); err != nil {
			logger.Warn("memory log write failed", "err", err)
		}
	}
}

func takeSample(controller int) (memorySample, error) {
	sample := memorySample{Time: time.Now().UTC()}

	if controller == 0 {
		return sample, nil
	}

	processes, err := readProcessTable()
	if err != nil {
		return sample, err
	}

	tree := append(
		[]processInfo{{PID: controller}},
		descendants(processes, controller)...,
	)

	for _, process := range tree {
		resident, err := residentBytes(process.PID)
		if err != nil {
			continue
		}

		command := ""
		if len(process.Args) > 0 {
			command = filepath.Base(process.Args[0])
		}

		sample.Processes = append(sample.Processes, processMemory{
			PID:      process.PID,
			Command:  command,
			Resident: resident,
		})
		sample.Total += resident

		switch {
		case process.PID == controller:
			sample.Controller = resident
		case isWorker(process.Args):
			sample.Workers += resident
		}
	}

	return sample, nil
}

// storedFrame is one feed frame written to a frames file, in the shape of a
// live WebSocket frame, so graders read exec and scenario trials alike.
type storedFrame struct {
	ID        string        `json:"id"`
	Type      string        `json:"type"`
	Data      any           `json:"data"`
	CreatedAt time.Time     `json:"createdAt"`
	Metadata  frameMetadata `json:"metadata"`
}

type frameMetadata struct {
	Sequence  int64   `json:"sequence"`
	SessionID string  `json:"sessionId"`
	RequestID *string `json:"requestId,omitempty"`
}

// dumpFeed writes a session's whole durable feed to path, oldest first.
func dumpFeed(
	ctx context.Context,
	client *controlclient.Client,
	sessionID uuid.UUID,
	path string,
) (int, error) {
	file, err := os.Create(path)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "create the frames file")
	}

	encoder := json.NewEncoder(file)
	written := 0
	cursor := int64(0)

	for {
		page, err := client.ListSessionFeed(ctx, sessionID, cursor)
		if err != nil {
			_ = file.Close() // The read error is the one to report.

			return written, ctxerrors.Wrap(err, "read the session feed")
		}

		for _, item := range page.Items {
			if err := encoder.Encode(feedFrame(item)); err != nil {
				_ = file.Close() // The write error is the one to report.

				return written, ctxerrors.Wrap(err, "write a frame")
			}

			written++
		}

		if !page.HasMore || page.NextCursor <= cursor {
			break
		}

		cursor = page.NextCursor
	}

	if err := file.Close(); err != nil {
		return written, ctxerrors.Wrap(err, "close the frames file")
	}

	return written, nil
}

func feedFrame(item feedItem) storedFrame {
	frame := storedFrame{
		ID:        item.Id.String(),
		Type:      item.Type,
		Data:      item.Data,
		CreatedAt: item.CreatedAt,
		Metadata: frameMetadata{
			Sequence:  item.Sequence,
			SessionID: item.SessionId.String(),
		},
	}

	if item.RequestId != nil {
		requestID := item.RequestId.String()
		frame.Metadata.RequestID = &requestID
	}

	return frame
}
