package docker

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const (
	// dockerFrameHeaderBytes is the Engine API's log frame header: one stream
	// type byte, three reserved zero bytes, then a big-endian payload length.
	dockerFrameHeaderBytes = 8

	// dockerFrameSizeOffset is where the payload length starts in that header.
	dockerFrameSizeOffset = 4

	// maxLogFrameBytes refuses an implausible frame length rather than letting
	// the daemon's byte count decide this process's allocation.
	maxLogFrameBytes = 16 << 20
)

// logDemultiplexer unwraps the Engine API's multiplexed container log stream.
//
// A container created without a TTY, which every worker is, has its stdout and
// stderr framed rather than sent raw. Reading the stream directly would feed
// frame headers into the log relay as though they were record text.
//
// Both streams arrive as one sequence, because a worker's own records and the
// output of the commands it runs belong in the relayed order they happened.
type logDemultiplexer struct {
	source io.Reader

	// remaining is int64 so every conversion in this reader widens. A uint32
	// counter forces narrowing from len() and Read(), which is exactly the
	// shape a truncation bug hides in.
	remaining int64
	header    [dockerFrameHeaderBytes]byte
}

func newLogDemultiplexer(source io.Reader) *logDemultiplexer {
	return &logDemultiplexer{source: source}
}

func (d *logDemultiplexer) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}

	// A zero-length frame is legal and carries nothing, so keep reading
	// headers until one announces a payload.
	for d.remaining == 0 {
		if err := d.readHeader(); err != nil {
			return 0, err
		}
	}

	if int64(len(destination)) > d.remaining {
		destination = destination[:d.remaining]
	}

	read, err := d.source.Read(destination)
	if read > 0 {
		d.remaining -= int64(read)
	}

	if err != nil {
		// The stream ending is the worker exiting, which callers treat as the
		// normal end rather than a fault.
		if errors.Is(err, io.EOF) {
			return read, io.EOF
		}

		return read, ctxerrors.Wrap(err, "read the container log frame")
	}

	return read, nil
}

func (d *logDemultiplexer) readHeader() error {
	if _, err := io.ReadFull(d.source, d.header[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return io.EOF
		}

		return ctxerrors.Wrap(err, "read the container log frame header")
	}

	size := binary.BigEndian.Uint32(d.header[dockerFrameSizeOffset:])
	if size > maxLogFrameBytes {
		return ctxerrors.Wrapf(
			commerr.ErrParseFailed,
			"container log frame announces %d bytes",
			size,
		)
	}

	d.remaining = int64(size)

	return nil
}
