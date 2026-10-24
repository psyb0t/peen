package tracing

import (
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func WithSpanError(span trace.Span, err error) error {
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	}

	// Annotating a span must not change the error's concrete protocol type.
	return err
}
