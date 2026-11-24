package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/psyb0t/ctxerrors"
)

// prepareLongResponse removes the connection write deadline for this request.
// In-memory writers may have no deadline support because they have no socket.
func prepareLongResponse(writer http.ResponseWriter) error {
	err := http.NewResponseController(writer).SetWriteDeadline(time.Time{})
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		return ctxerrors.Wrap(err, "extend response write deadline")
	}

	return nil
}
