package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/psyb0t/ctxerrors"
)

// strictJSONSerializer applies the OpenAPI object contract at every JSON
// request boundary, including nested objects and trailing values.
type strictJSONSerializer struct{}

func (strictJSONSerializer) Serialize(
	ctx echo.Context,
	value any,
	indent string,
) error {
	serializer := echo.DefaultJSONSerializer{}
	if err := serializer.Serialize(ctx, value, indent); err != nil {
		return ctxerrors.Wrap(err, "serialize JSON response")
	}

	return nil
}

func (strictJSONSerializer) Deserialize(
	ctx echo.Context,
	value any,
) error {
	decoder := json.NewDecoder(ctx.Request().Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(value); err != nil {
		return invalidJSONBodyError()
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return invalidJSONBodyError()
	}

	return nil
}

func invalidJSONBodyError() *echo.HTTPError {
	return echo.NewHTTPError(http.StatusBadRequest, invalidJSONBodyMessage)
}

var _ echo.JSONSerializer = strictJSONSerializer{}
