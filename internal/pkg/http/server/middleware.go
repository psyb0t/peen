package server

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/aichteeteapee/serbewr/middleware"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/metrics"
)

func normalizeRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get(headerRequestID)
		if provided == "" {
			next.ServeHTTP(w, r)

			return
		}

		if _, err := uuid.Parse(provided); err == nil {
			next.ServeHTTP(w, r)

			return
		}

		request := r.Clone(r.Context())
		request.Header.Del(headerRequestID)
		next.ServeHTTP(w, request)
	})
}

func (s *Server) requestMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()

		s.deps.Metrics.HTTPStarted()

		writer := &metricsResponseWriter{
			BaseResponseWriter: middleware.BaseResponseWriter{
				ResponseWriter: w,
			},
			status: http.StatusOK,
		}
		next.ServeHTTP(writer, r)

		outcome := metrics.OutcomeSuccess
		if writer.status >= http.StatusInternalServerError {
			outcome = metrics.OutcomeError
		}

		s.deps.Metrics.HTTPCompleted(
			r.Method,
			metricRoute(r.URL.Path),
			strconv.Itoa(writer.status),
			outcome,
			time.Since(startedAt),
		)
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.deps.APIToken == "" {
			next.ServeHTTP(w, r)

			return
		}

		isBearerTokenValid := isValidBearerToken(
			r.Header.Get(headerAuthorization),
			s.deps.APIToken,
		)

		isWebSocketTokenValid := r.URL.Path == webSocketPath &&
			isValidWebSocketBearerToken(
				r.Header.Values(headerWebSocketProtocol),
				s.deps.APIToken,
			)
		if !isBearerTokenValid && !isWebSocketTokenValid {
			writeAPIError(
				w,
				http.StatusUnauthorized,
				aichteeteapee.ErrorCodeUnauthorized,
				invalidBearerTokenMessage,
			)

			return
		}

		next.ServeHTTP(w, r)
	})
}

func isValidWebSocketBearerToken(
	protocolHeaders []string,
	expectedToken string,
) bool {
	for _, header := range protocolHeaders {
		for protocol := range strings.SplitSeq(header, ",") {
			encodedToken, found := strings.CutPrefix(
				strings.TrimSpace(protocol),
				webSocketBearerSubprotocolPrefix,
			)
			if !found {
				continue
			}

			token, err := base64.RawURLEncoding.DecodeString(encodedToken)
			if err != nil {
				continue
			}

			if subtle.ConstantTimeCompare(token, []byte(expectedToken)) == 1 {
				return true
			}
		}
	}

	return false
}

func isValidBearerToken(authorization string, expectedToken string) bool {
	scheme, token, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return false
	}

	return subtle.ConstantTimeCompare(
		[]byte(token),
		[]byte(expectedToken),
	) == 1
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maximumRequestBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// validateJSONBody rejects malformed or concatenated JSON values before the
// generated binder reads the request. encoding/json accepts a valid first
// value by default, which would otherwise let a second request body through.
func validateJSONBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasJSONRequestBody(r) {
			next.ServeHTTP(w, r)

			return
		}

		originalBody := r.Body

		body, readErr := io.ReadAll(originalBody)
		if closeErr := originalBody.Close(); closeErr != nil {
			ctxscope.GetLogger(r.Context()).Warn(
				"closing HTTP request body failed",
				"err", closeErr,
			)
		}

		if readErr != nil {
			ctxscope.GetLogger(r.Context()).Warn(
				"reading HTTP JSON body failed",
				"err", readErr,
			)
			writeAPIError(
				w,
				http.StatusBadRequest,
				aichteeteapee.ErrorCodeValidationFailed,
				invalidJSONBodyMessage,
			)

			return
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		if isSingleJSONValue(body) {
			next.ServeHTTP(w, r)

			return
		}

		ctxscope.GetLogger(r.Context()).Debug("HTTP JSON body rejected")
		writeAPIError(
			w,
			http.StatusBadRequest,
			aichteeteapee.ErrorCodeValidationFailed,
			invalidJSONBodyMessage,
		)
	})
}

func hasJSONRequestBody(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		mediaType, _, _ := strings.Cut(
			r.Header.Get(headerContentType),
			";",
		)

		return strings.EqualFold(strings.TrimSpace(mediaType), mediaTypeJSON)
	default:
		return false
	}
}

func isSingleJSONValue(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))

	value := json.RawMessage{}
	if err := decoder.Decode(&value); err != nil {
		return false
	}

	return errors.Is(decoder.Decode(&value), io.EOF)
}

func negotiateResponse(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptedRepresentation(r.Header.Get(headerAccept)) {
			writeAPIError(
				w,
				http.StatusNotAcceptable,
				aichteeteapee.ErrorCodeBadRequest,
				unsupportedResponseMessage,
			)

			return
		}

		next.ServeHTTP(w, r)
	})
}

func acceptedRepresentation(accept string) bool {
	if accept == "" {
		return true
	}

	for value := range strings.SplitSeq(accept, ",") {
		mediaType, _, _ := strings.Cut(strings.TrimSpace(value), ";")
		switch mediaType {
		case mediaTypeAny, mediaTypeJSON:
			return true
		}
	}

	return false
}

type metricsResponseWriter struct {
	middleware.BaseResponseWriter
	status      int
	wroteHeader bool
}

func (w *metricsResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}

	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *metricsResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	written, err := w.ResponseWriter.Write(body)
	if err != nil {
		return written, ctxerrors.Wrap(err, "write HTTP response")
	}

	return written, nil
}

func metricRoute(path string) string {
	if route := staticMetricRoute(path); route != "" {
		return route
	}

	return dynamicMetricRoute(path)
}

func staticMetricRoute(path string) string {
	switch path {
	case healthzPath,
		readyPath,
		webSocketPath,
		apiBaseURL + "/messages",
		apiBaseURL + "/session",
		apiBaseURL + "/session/cancel",
		apiBaseURL + "/session/events",
		apiBaseURL + "/session/jobs",
		apiBaseURL + "/session/agents":
		return path
	default:
		return ""
	}
}

func dynamicMetricRoute(path string) string {
	switch {
	case strings.HasPrefix(path, apiBaseURL+"/session/jobs/") &&
		strings.HasSuffix(path, "/output"):
		return apiBaseURL + "/session/jobs/{jobId}/output"
	case strings.HasPrefix(path, apiBaseURL+"/session/jobs/") &&
		strings.HasSuffix(path, "/signal"):
		return apiBaseURL + "/session/jobs/{jobId}/signal"
	case strings.HasPrefix(path, apiBaseURL+"/session/agents/") &&
		strings.HasSuffix(path, "/messages"):
		return apiBaseURL + "/session/agents/{agentRunId}/messages"
	case strings.HasPrefix(path, apiBaseURL+"/session/agents/") &&
		strings.HasSuffix(path, "/cancel"):
		return apiBaseURL + "/session/agents/{agentRunId}/cancel"
	default:
		return metricRouteUnmatched
	}
}
