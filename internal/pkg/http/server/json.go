package server

import (
	"net/http"

	"github.com/psyb0t/aichteeteapee"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

func writeAPIError(
	w http.ResponseWriter,
	status int,
	code aichteeteapee.ErrorCode,
	message string,
) {
	aichteeteapee.WriteJSON(w, status, api.Error{Code: code, Message: message})
}
