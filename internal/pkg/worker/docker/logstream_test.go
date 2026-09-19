package docker

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testStreamStdout = 1
	testStreamStderr = 2
)

// TestLogDemultiplexerJoinsBothStreams checks the framing the Engine API uses
// for a container without a TTY, which every worker is.
//
// Reading that stream raw would feed eight header bytes into the log relay in
// front of every record, so the records would never match what an operator
// searches the audit trail for.
func TestLogDemultiplexerJoinsBothStreams(t *testing.T) {
	t.Parallel()

	framed := &bytes.Buffer{}
	writeTestFrame(t, framed, testStreamStdout, `{"msg":"hook action started"}`+"\n")
	writeTestFrame(t, framed, testStreamStderr, "go: downloading\n")
	writeTestFrame(t, framed, testStreamStdout, `{"msg":"skill activated"}`+"\n")

	decoded, err := io.ReadAll(newLogDemultiplexer(framed))
	require.NoError(t, err)

	assert.Equal(
		t,
		`{"msg":"hook action started"}`+"\n"+
			"go: downloading\n"+
			`{"msg":"skill activated"}`+"\n",
		string(decoded),
	)
}

// TestLogDemultiplexerSkipsEmptyFrames keeps a zero-length frame, which the
// daemon is allowed to send, from ending the stream early.
func TestLogDemultiplexerSkipsEmptyFrames(t *testing.T) {
	t.Parallel()

	framed := &bytes.Buffer{}
	writeTestFrame(t, framed, testStreamStdout, "")
	writeTestFrame(t, framed, testStreamStdout, "after the empty frame\n")

	decoded, err := io.ReadAll(newLogDemultiplexer(framed))
	require.NoError(t, err)
	assert.Equal(t, "after the empty frame\n", string(decoded))
}

// TestLogDemultiplexerSplitsAcrossReads covers a payload larger than the
// caller's buffer, so a long tool result is not truncated at a frame boundary.
func TestLogDemultiplexerSplitsAcrossReads(t *testing.T) {
	t.Parallel()

	payload := strings.Repeat("a", 5000)
	framed := &bytes.Buffer{}
	writeTestFrame(t, framed, testStreamStdout, payload)

	decoded := &bytes.Buffer{}
	_, err := io.CopyBuffer(decoded, newLogDemultiplexer(framed), make([]byte, 64))
	require.NoError(t, err)
	assert.Equal(t, payload, decoded.String())
}

// TestLogDemultiplexerRefusesAnImplausibleFrame keeps a daemon byte count from
// deciding this process's allocation.
func TestLogDemultiplexerRefusesAnImplausibleFrame(t *testing.T) {
	t.Parallel()

	header := make([]byte, dockerFrameHeaderBytes)
	header[0] = testStreamStdout
	binary.BigEndian.PutUint32(header[dockerFrameSizeOffset:], maxLogFrameBytes+1)

	_, err := io.ReadAll(newLogDemultiplexer(bytes.NewReader(header)))
	require.ErrorIs(t, err, commerr.ErrParseFailed)
}

// TestLogDemultiplexerEndsOnATruncatedHeader covers a daemon connection that
// drops partway through a header, which is a worker dying mid-write.
func TestLogDemultiplexerEndsOnATruncatedHeader(t *testing.T) {
	t.Parallel()

	_, err := io.ReadAll(newLogDemultiplexer(bytes.NewReader([]byte{1, 0, 0})))
	require.Error(t, err)
}

func writeTestFrame(
	t *testing.T,
	into *bytes.Buffer,
	stream byte,
	payload string,
) {
	t.Helper()

	require.LessOrEqual(t, len(payload), math.MaxUint32)

	header := make([]byte, dockerFrameHeaderBytes)
	header[0] = stream
	binary.BigEndian.PutUint32(
		header[dockerFrameSizeOffset:],
		//nolint:gosec // Bounded by the assertion above.
		uint32(len(payload)),
	)

	_, err := into.Write(header)
	require.NoError(t, err)

	_, err = into.WriteString(payload)
	require.NoError(t, err)
}
