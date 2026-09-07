package elelemstream

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey"
)

// roundStream tracks one round's thinking + text streamers. A round's
// thinking block, if any, MUST close before its text block opens — a client
// renders the two apart, so interleaving them would corrupt both.
type roundStream struct {
	publisher *essessey.Publisher
	thinking  *essessey.TextStreamer
	text      *essessey.TextStreamer
}

// newRoundStream opens a roundStream at base. The thinking streamer is built
// eagerly but emits nothing until it receives content — block opening stays
// lazy regardless.
func newRoundStream(publisher *essessey.Publisher, base int) *roundStream {
	return &roundStream{
		publisher: publisher,
		thinking:  essessey.NewThinkingStreamer(publisher, base),
	}
}

// openText closes the thinking block (a no-op if it never opened) and starts
// the text streamer at whatever index that left the thinking streamer on.
func (stream *roundStream) openText(ctx context.Context) error {
	if stream.text != nil {
		return nil
	}

	if err := stream.thinking.Close(ctx); err != nil {
		return ctxerrors.Wrap(err, "close thinking block")
	}

	stream.text = essessey.NewTextStreamer(
		stream.publisher,
		stream.thinking.BlockIndex(),
	)

	return nil
}

// handleDelta routes one streamed chunk: reasoning goes to the thinking
// streamer as long as text has not opened yet, text always closes thinking
// first via openText.
func (stream *roundStream) handleDelta(
	ctx context.Context,
	delta elelem.Delta,
) error {
	if delta.Reasoning != "" && stream.text == nil {
		if err := stream.thinking.Write(ctx, delta.Reasoning); err != nil {
			return ctxerrors.Wrap(err, "write reasoning delta")
		}
	}

	if delta.Text == "" {
		return nil
	}

	if err := stream.openText(ctx); err != nil {
		return ctxerrors.Wrap(err, "open text block for delta")
	}

	if err := stream.text.Write(ctx, delta.Text); err != nil {
		return ctxerrors.Wrap(err, "write text delta")
	}

	return nil
}

// finish closes out the round: it opens (if needed) and closes the text
// block, guaranteeing thinking is already closed by the time it returns.
func (stream *roundStream) finish(ctx context.Context) error {
	if err := stream.openText(ctx); err != nil {
		return ctxerrors.Wrap(err, "open text block to finish round")
	}

	if err := stream.text.Close(ctx); err != nil {
		return ctxerrors.Wrap(err, "close text block")
	}

	return nil
}

// nextBlockIndex reports where the NEXT content block should start, whether
// or not text ever opened this round.
func (stream *roundStream) nextBlockIndex() int {
	if stream.text == nil {
		return stream.thinking.BlockIndex()
	}

	return stream.text.BlockIndex()
}
