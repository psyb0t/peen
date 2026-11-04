package tools

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
)

const (
	webFetchMaxRedirects = 10
	webFetchUserAgent    = "Peen-webFetch/1"
	webFetchAccept       = "text/html, text/plain;q=0.9, */*;q=0.5"
	// webFetchSniffBytes is how much of a body net/http's sniffer reads to
	// guess a missing content type.
	webFetchSniffBytes = 512

	schemeHTTP  = "http"
	schemeHTTPS = "https"

	mediaTypeXHTML      = "application/xhtml+xml"
	mediaTypeJavaScript = "application/javascript"
	mediaTypeTextPrefix = "text/"
	mediaTypeJSONSuffix = "+json"
	mediaTypeXMLSuffix  = "+xml"
)

// WebFetch reads an http or https URL from the worker's network and returns
// its body as text. An HTML page is reduced to its readable text. Other text
// formats come back as they are, and any other content type is refused. At
// most Limits.MaxFetchBytes of the body is read. A non-2xx status is not an
// error: the output carries the status and whatever text the server sent.
func (e *Executor) WebFetch(
	ctx context.Context,
	input WebFetchInput,
) (WebFetchOutput, error) {
	target, err := webFetchURL(input.URL)
	if err != nil {
		return WebFetchOutput{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		target.String(), nil)
	if err != nil {
		return WebFetchOutput{}, ctxerrors.Wrap(err, "build fetch request")
	}

	request.Header.Set(aichteeteapee.HeaderNameUserAgent, webFetchUserAgent)
	request.Header.Set(aichteeteapee.HeaderNameAccept, webFetchAccept)

	client := &http.Client{CheckRedirect: checkWebFetchRedirect}

	response, err := client.Do(request)
	if err != nil {
		return WebFetchOutput{}, webFetchError(ctx, err)
	}

	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			ctxscope.GetLogger(ctx).Debug("close fetch response failed",
				"err", closeErr)
		}
	}()

	body, truncated, err := readWebFetchBody(response.Body,
		e.limits.MaxFetchBytes)
	if err != nil {
		return WebFetchOutput{}, webFetchError(ctx, err)
	}

	return webFetchOutput(response, body, truncated)
}

func webFetchURL(raw string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, ctxerrors.Wrap(
			errors.Join(commerr.ErrValidationFailed, ErrUnsupportedURL),
			"parse fetch URL",
		)
	}

	if !isWebFetchScheme(target.Scheme) || target.Host == "" {
		return nil, ctxerrors.Wrap(
			errors.Join(commerr.ErrValidationFailed, ErrUnsupportedURL),
			"check fetch URL",
		)
	}

	return target, nil
}

func isWebFetchScheme(scheme string) bool {
	scheme = strings.ToLower(scheme)

	return scheme == schemeHTTP || scheme == schemeHTTPS
}

func checkWebFetchRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= webFetchMaxRedirects {
		return ctxerrors.Wrapf(commerr.ErrFetchFailed,
			"stopped after %d redirects", webFetchMaxRedirects)
	}

	if !isWebFetchScheme(request.URL.Scheme) {
		return ctxerrors.Wrap(
			errors.Join(commerr.ErrValidationFailed, ErrUnsupportedURL),
			"follow fetch redirect",
		)
	}

	return nil
}

// webFetchError keeps cancellation as it is, so a stopped turn ends the call,
// and marks every other failure as a failed fetch the model can read.
func webFetchError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctxerrors.Wrap(ctx.Err(), "fetch canceled")
	}

	if errors.Is(err, ErrUnsupportedURL) {
		return ctxerrors.Wrap(err, "fetch URL")
	}

	return ctxerrors.Wrap(errors.Join(commerr.ErrFetchFailed, err), "fetch URL")
}

func readWebFetchBody(body io.Reader, limit int) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		return nil, false, ctxerrors.Wrap(err, "read fetch response")
	}

	if len(data) <= limit {
		return data, false, nil
	}

	return data[:limit], true, nil
}

func webFetchOutput(
	response *http.Response,
	body []byte,
	truncated bool,
) (WebFetchOutput, error) {
	contentType := response.Header.Get(aichteeteapee.HeaderNameContentType)
	if contentType == "" {
		contentType = http.DetectContentType(body[:min(len(body),
			webFetchSniffBytes)])
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(contentType))
	}

	output := WebFetchOutput{
		URL:         response.Request.URL.String(),
		Status:      response.StatusCode,
		ContentType: mediaType,
		Truncated:   truncated,
	}

	whole := trimPartialRune(body, webFetchBodyTotal(body, truncated))
	text := strings.ToValidUTF8(string(whole), string(utf8.RuneError))

	switch {
	case isHTMLMediaType(mediaType):
		page := htmlText(text, response.Request.URL)
		output.Title, output.Text = page.title, page.text
	case isTextMediaType(mediaType):
		output.Text = text
	default:
		return WebFetchOutput{}, ctxerrors.Wrapf(ErrBinaryContent,
			"content type %s", mediaType)
	}

	return output, nil
}

// webFetchBodyTotal is the length trimPartialRune compares against: a body
// cut at the limit is shorter than the response, so its last rune may be cut.
func webFetchBodyTotal(body []byte, truncated bool) int {
	if truncated {
		return len(body) + 1
	}

	return len(body)
}

func isHTMLMediaType(mediaType string) bool {
	return mediaType == aichteeteapee.ContentTypeHTML ||
		mediaType == mediaTypeXHTML
}

func isTextMediaType(mediaType string) bool {
	switch mediaType {
	case aichteeteapee.ContentTypeJSON, aichteeteapee.ContentTypeXML,
		aichteeteapee.ContentTypeYAML, aichteeteapee.ContentTypeXYAML,
		mediaTypeJavaScript:
		return true
	}

	return strings.HasPrefix(mediaType, mediaTypeTextPrefix) ||
		strings.HasSuffix(mediaType, mediaTypeJSONSuffix) ||
		strings.HasSuffix(mediaType, mediaTypeXMLSuffix)
}
