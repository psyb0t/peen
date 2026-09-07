package tools

import "sync"

// jobBuffer is a bounded ring of output lines for one stream of one job,
// fed from commander's per-line Stream channel. It is bounded by BOTH line
// count and total bytes; once either bound is exceeded, whole lines are
// dropped from the front (oldest first) until both are satisfied again, or
// only one line remains. Every line ever appended gets a permanent,
// monotonically increasing sequence number, so a caller's cursor keeps its
// meaning even after older lines are evicted; Read reports how many lines
// were skipped because they were already gone.
//
// Safe for concurrent use: one goroutine appends from commander's stream
// channel while readers take a snapshot at any time.
type jobBuffer struct {
	mu       sync.Mutex
	maxLines int
	maxBytes int
	lines    []string
	byteLen  int
	total    int
}

func newJobBuffer(maxLines, maxBytes int) *jobBuffer {
	return &jobBuffer{maxLines: maxLines, maxBytes: maxBytes}
}

// Append records one more line. It never blocks: the caller is commander's
// stream consumer goroutine, which must never stall commander's broadcast.
func (b *jobBuffer) Append(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.lines = append(b.lines, line)
	b.byteLen += len(line)
	b.total++

	for len(b.lines) > 1 && b.overBoundLocked() {
		b.byteLen -= len(b.lines[0])
		b.lines = b.lines[1:]
	}
}

func (b *jobBuffer) overBoundLocked() bool {
	return len(b.lines) > b.maxLines || b.byteLen > b.maxBytes
}

// firstIndexLocked is the sequence number of lines[0], the oldest line
// still held. Caller must hold b.mu.
func (b *jobBuffer) firstIndexLocked() int {
	return b.total - len(b.lines)
}

// Read returns up to maxLines lines starting at cursor, the cursor to
// resume from on the next call, and how many lines were skipped because
// they had already been evicted before cursor. maxLines <= 0 means no cap.
func (b *jobBuffer) Read(cursor, maxLines int) ([]string, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	first := b.firstIndexLocked()

	dropped := 0
	if cursor < first {
		dropped = first - cursor
		cursor = first
	}

	offset := min(cursor-first, len(b.lines))

	available := b.lines[offset:]
	if maxLines > 0 && len(available) > maxLines {
		available = available[:maxLines]
	}

	result := append([]string(nil), available...)

	return result, cursor + len(result), dropped
}

// All returns every currently buffered line and the total dropped count.
func (b *jobBuffer) All() ([]string, int) {
	lines, _, dropped := b.Read(0, 0)

	return lines, dropped
}

// Stats reports how many lines are currently buffered and how many have
// been dropped since the buffer was created.
func (b *jobBuffer) Stats() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return len(b.lines), b.total - len(b.lines)
}
