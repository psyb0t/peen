package tools

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	jobBufferTestMaxLines    = 3
	jobBufferTestMaxBytes    = 1024
	jobBufferSmallBytes      = 4
	jobBufferConcurrentGoros = 8
	jobBufferWritesPerGoro   = 64
	jobBufferConcurrentLines = 16
)

func TestJobBuffer_AppendAndAll(t *testing.T) {
	t.Parallel()

	buf := newJobBuffer(jobBufferTestMaxLines, jobBufferTestMaxBytes)

	buf.Append("one")
	buf.Append("two")

	lines, dropped := buf.All()

	assert.Equal(t, []string{"one", "two"}, lines)
	assert.Zero(t, dropped)
}

func TestJobBuffer_DropsOldestOverLineBound(t *testing.T) {
	t.Parallel()

	buf := newJobBuffer(jobBufferTestMaxLines, jobBufferTestMaxBytes)

	for i := range 5 {
		buf.Append(strconv.Itoa(i))
	}

	lines, dropped := buf.All()

	assert.Equal(t, []string{"2", "3", "4"}, lines)
	assert.Equal(t, 2, dropped)

	buffered, statsDropped := buf.Stats()
	assert.Equal(t, 3, buffered)
	assert.Equal(t, 2, statsDropped)
}

func TestJobBuffer_DropsOldestOverByteBound(t *testing.T) {
	t.Parallel()

	buf := newJobBuffer(jobBufferTestMaxLines*10, jobBufferSmallBytes)

	buf.Append("aa")
	buf.Append("bb")
	buf.Append("cc")

	lines, dropped := buf.All()

	assert.Equal(t, []string{"bb", "cc"}, lines)
	assert.Equal(t, 1, dropped)
}

func TestJobBuffer_KeepsSoleOversizedLine(t *testing.T) {
	t.Parallel()

	buf := newJobBuffer(jobBufferTestMaxLines, jobBufferSmallBytes)

	buf.Append("way too long for the byte bound")

	lines, dropped := buf.All()

	require.Len(t, lines, 1)
	assert.Zero(t, dropped)
}

func TestJobBuffer_ReadCursorIncremental(t *testing.T) {
	t.Parallel()

	buf := newJobBuffer(jobBufferTestMaxLines*10, jobBufferTestMaxBytes)

	buf.Append("a")
	buf.Append("b")
	buf.Append("c")

	firstLines, firstCursor, firstDropped := buf.Read(0, 2)
	assert.Equal(t, []string{"a", "b"}, firstLines)
	assert.Equal(t, 2, firstCursor)
	assert.Zero(t, firstDropped)

	secondLines, secondCursor, secondDropped := buf.Read(firstCursor, 2)
	assert.Equal(t, []string{"c"}, secondLines)
	assert.Equal(t, 3, secondCursor)
	assert.Zero(t, secondDropped)

	thirdLines, thirdCursor, thirdDropped := buf.Read(secondCursor, 2)
	assert.Empty(t, thirdLines)
	assert.Equal(t, secondCursor, thirdCursor)
	assert.Zero(t, thirdDropped)
}

func TestJobBuffer_ReadReportsDroppedBeforeCursor(t *testing.T) {
	t.Parallel()

	buf := newJobBuffer(jobBufferTestMaxLines, jobBufferTestMaxBytes)

	for i := range 5 {
		buf.Append(strconv.Itoa(i))
	}

	lines, next, dropped := buf.Read(0, 0)

	assert.Equal(t, []string{"2", "3", "4"}, lines)
	assert.Equal(t, 5, next)
	assert.Equal(t, 2, dropped)
}

func TestJobBuffer_ReadZeroMaxLinesReturnsEverythingAvailable(t *testing.T) {
	t.Parallel()

	buf := newJobBuffer(jobBufferTestMaxLines*10, jobBufferTestMaxBytes)

	buf.Append("x")
	buf.Append("y")

	lines, next, dropped := buf.Read(0, 0)

	assert.Equal(t, []string{"x", "y"}, lines)
	assert.Equal(t, 2, next)
	assert.Zero(t, dropped)
}

// TestJobBuffer_ConcurrentAppendAndRead mirrors production usage: one
// goroutine per stream appends (as commander's stream consumer does) while
// another concurrently reads, the way read_job_output/wait_job can observe
// a still-running job. Run with -race.
func TestJobBuffer_ConcurrentAppendAndRead(t *testing.T) {
	t.Parallel()

	buf := newJobBuffer(jobBufferConcurrentLines, jobBufferTestMaxBytes)

	var wg sync.WaitGroup

	for range jobBufferConcurrentGoros {
		wg.Go(func() {
			for i := range jobBufferWritesPerGoro {
				buf.Append(strconv.Itoa(i))
			}
		})
	}

	wg.Go(func() {
		for range jobBufferWritesPerGoro {
			buf.Read(0, 0)
			buf.Stats()
			buf.All()
		}
	})

	wg.Wait()

	buffered, dropped := buf.Stats()
	assert.LessOrEqual(t, buffered, jobBufferConcurrentLines)
	assert.GreaterOrEqual(t, dropped, 0)
}
