package server

import "errors"

// ErrMissingDependency reports an incomplete HTTP server constructor input.
var ErrMissingDependency = errors.New("HTTP server dependency is required")
