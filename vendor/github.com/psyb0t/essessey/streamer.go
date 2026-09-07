package essessey

import (
	"context"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

// streamerKind selects which content-block type a TextStreamer emits: ordinary
// answer text, or a reasoning (thinking) block.
type streamerKind int

const (
	streamerText streamerKind = iota
	streamerThinking
)

// TextStreamer accumulates streamed text and, when a Publisher is set, emits it
// as content blocks — text blocks by default, or thinking blocks when built via
// NewThinkingStreamer. Publisher may be nil for non-streaming use — the text
// still accumulates and is readable via Text.
//
// It also owns the block index, which is why block accounting stays consistent
// across a turn: Close advances the index exactly once per closed block, so the
// next streamer starts where this one stopped.
type TextStreamer struct {
	publisher      *Publisher
	kind           streamerKind
	messageBuilder strings.Builder
	blockIndex     int
	blockStarted   bool
}

// NewTextStreamer builds a TextStreamer emitting text blocks from startIndex.
func NewTextStreamer(publisher *Publisher, startIndex int) *TextStreamer {
	return &TextStreamer{
		publisher:  publisher,
		kind:       streamerText,
		blockIndex: startIndex,
	}
}

// NewThinkingStreamer builds a TextStreamer emitting reasoning (thinking)
// blocks from startIndex. Same accumulation + block-index accounting as the
// text streamer; only the emitted block/delta type differs.
func NewThinkingStreamer(publisher *Publisher, startIndex int) *TextStreamer {
	return &TextStreamer{
		publisher:  publisher,
		kind:       streamerThinking,
		blockIndex: startIndex,
	}
}

// startBlock emits the content_block_start for this streamer's kind.
func (s *TextStreamer) startBlock() error {
	if s.kind == streamerThinking {
		return s.publisher.SendContentBlockStartThinking(s.blockIndex)
	}

	return s.publisher.SendContentBlockStartText(s.blockIndex)
}

// deltaBlock emits a content_block_delta for this streamer's kind.
func (s *TextStreamer) deltaBlock(chunk string) error {
	if s.kind == streamerThinking {
		return s.publisher.SendContentBlockDeltaThinking(s.blockIndex, chunk)
	}

	return s.publisher.SendContentBlockDeltaText(s.blockIndex, chunk)
}

// Text returns the accumulated text.
func (s *TextStreamer) Text() string {
	return s.messageBuilder.String()
}

// BlockIndex returns the next available content-block index.
func (s *TextStreamer) BlockIndex() int {
	return s.blockIndex
}

// BlockStarted reports whether a block is currently open.
func (s *TextStreamer) BlockStarted() bool {
	return s.blockStarted
}

// Write appends a chunk, opening the block on the first non-empty chunk and
// emitting a delta when a Publisher is set.
//
// Opening LAZILY is deliberate: a round that produces no text of its kind must
// not emit an empty block, or the client renders a stray blank card and the
// index advances for nothing.
func (s *TextStreamer) Write(_ context.Context, chunk string) error {
	if chunk == "" {
		return nil
	}

	s.messageBuilder.WriteString(chunk)

	if s.publisher == nil {
		return nil
	}

	if !s.blockStarted {
		if err := s.startBlock(); err != nil {
			return ctxerrors.Wrap(err, "send content block start")
		}

		s.blockStarted = true
	}

	if err := s.deltaBlock(chunk); err != nil {
		return ctxerrors.Wrap(err, "send content block delta")
	}

	return nil
}

// Close emits content_block_stop when a block is open, then advances the block
// index. A no-op when no block is open — which is what keeps the index honest
// for a streamer that never received content.
func (s *TextStreamer) Close(_ context.Context) error {
	if !s.blockStarted {
		return nil
	}

	if s.publisher != nil {
		err := s.publisher.SendContentBlockStop(s.blockIndex)
		if err != nil {
			return ctxerrors.Wrap(err, "send content block stop")
		}
	}

	s.blockStarted = false
	s.blockIndex++

	return nil
}

// LineTransform rewrites one buffered line before it is emitted. Returning the
// line unchanged is the identity transform.
type LineTransform func(line string) string

// LineStreamer is the newline-buffered sibling of TextStreamer: it splits input
// at newlines and passes each complete line through a LineTransform before
// emitting. Used for streaming line-oriented payloads (e.g. UI-component specs)
// where each line must be validated / rewritten before it reaches the client.
type LineStreamer struct {
	publisher    *Publisher
	transform    LineTransform
	lineBuffer   strings.Builder
	output       strings.Builder
	blockIndex   int
	blockStarted bool
}

// NewLineStreamer builds a LineStreamer emitting from startIndex. transform may
// be nil, in which case lines pass through unchanged.
func NewLineStreamer(
	publisher *Publisher,
	startIndex int,
	transform LineTransform,
) *LineStreamer {
	return &LineStreamer{
		publisher:  publisher,
		transform:  transform,
		blockIndex: startIndex,
	}
}

// Text returns the transformed output accumulated so far.
func (s *LineStreamer) Text() string {
	return s.output.String()
}

// BlockIndex returns the next available content-block index.
func (s *LineStreamer) BlockIndex() int {
	return s.blockIndex
}

// BlockStarted reports whether a block is currently open.
func (s *LineStreamer) BlockStarted() bool {
	return s.blockStarted
}

// Write buffers a chunk, emitting each complete (newline-terminated) line
// through the transform; a trailing partial line is held for the next chunk.
func (s *LineStreamer) Write(ctx context.Context, chunk string) error {
	if chunk == "" {
		return nil
	}

	rest := chunk

	for {
		nlIdx := strings.IndexByte(rest, '\n')
		if nlIdx < 0 {
			s.lineBuffer.WriteString(rest)

			return nil
		}

		s.lineBuffer.WriteString(rest[:nlIdx])
		line := s.lineBuffer.String()
		s.lineBuffer.Reset()

		if err := s.emitLine(ctx, line, true); err != nil {
			return ctxerrors.Wrap(err, "emit completed line")
		}

		rest = rest[nlIdx+1:]
	}
}

// Close flushes any trailing partial line and emits content_block_stop.
func (s *LineStreamer) Close(ctx context.Context) error {
	if tail := s.lineBuffer.String(); tail != "" {
		if err := s.emitLine(ctx, tail, false); err != nil {
			return ctxerrors.Wrap(err, "flush trailing line")
		}

		s.lineBuffer.Reset()
	}

	if !s.blockStarted {
		return nil
	}

	if s.publisher != nil {
		err := s.publisher.SendContentBlockStop(s.blockIndex)
		if err != nil {
			return ctxerrors.Wrap(err, "send content block stop")
		}
	}

	s.blockStarted = false
	s.blockIndex++

	return nil
}

// emitLine transforms a line, accumulates it, and forwards it to the publisher.
func (s *LineStreamer) emitLine(
	_ context.Context,
	line string,
	withNewline bool,
) error {
	rewritten := line
	if s.transform != nil {
		rewritten = s.transform(line)
	}

	chunk := rewritten
	if withNewline {
		chunk += "\n"
	}

	s.output.WriteString(chunk)

	if s.publisher == nil {
		return nil
	}

	if !s.blockStarted {
		err := s.publisher.SendContentBlockStartText(s.blockIndex)
		if err != nil {
			return ctxerrors.Wrap(err, "send content block start")
		}

		s.blockStarted = true
	}

	if err := s.publisher.SendContentBlockDeltaText(
		s.blockIndex,
		chunk,
	); err != nil {
		return ctxerrors.Wrap(err, "send content block delta")
	}

	return nil
}
