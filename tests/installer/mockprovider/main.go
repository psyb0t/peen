//go:build installer

// Command mockprovider is the installer suite's OpenAI-compatible model
// provider. The suite runs it in a container on the default Docker bridge,
// because controller and worker containers reach a container address on every
// host, while a host firewall may drop their traffic to a port on the host.
//
// It accepts one model and one key, so a completion proves the configured key
// reached whichever process made the call. GET /stats reports how many
// completions it served.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/psyb0t/ctxerrors"
)

const (
	readHeaderTimeout = 30 * time.Second
	contentTypeJSON   = "application/json"
	contentTypeStream = "text/event-stream"
	headerContentType = "Content-Type"
	headerAuth        = "Authorization"
	bearerPrefix      = "Bearer "
	finishReasonStop  = "stop"
	streamDone        = "data: [DONE]\n\n"
	fieldObject       = "object"
)

type provider struct {
	key         string
	model       string
	answer      string
	completions atomic.Int64
}

func main() {
	listen := flag.String("listen", ":8080", "listen address")
	key := flag.String("key", "", "the only accepted API key")
	model := flag.String("model", "", "the only listed model")
	answer := flag.String("answer", "", "the text every completion streams")

	flag.Parse()

	handler := &provider{key: *key, model: *model, answer: *answer}
	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	if err := server.ListenAndServe(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "mock provider stopped:", err)

		os.Exit(1)
	}
}

func (p *provider) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.URL.Path == "/stats" {
		p.writeJSON(writer, map[string]int64{
			"completions": p.completions.Load(),
		})

		return
	}

	if request.Header.Get(headerAuth) != bearerPrefix+p.key {
		http.Error(writer, "unknown key", http.StatusUnauthorized)

		return
	}

	switch strings.TrimPrefix(request.URL.Path, "/v1") {
	case "/models":
		p.writeJSON(writer, map[string]any{
			fieldObject: "list",
			"data": []map[string]string{
				{"id": p.model, fieldObject: "model"},
			},
		})
	case "/chat/completions":
		p.writeCompletion(writer)
	default:
		http.NotFound(writer, request)
	}
}

func (p *provider) writeJSON(writer http.ResponseWriter, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)

		return
	}

	writer.Header().Set(headerContentType, contentTypeJSON)
	_, _ = writer.Write(encoded)
}

func (p *provider) writeCompletion(writer http.ResponseWriter) {
	stream, err := p.completionStream()
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)

		return
	}

	p.completions.Add(1)
	writer.Header().Set(headerContentType, contentTypeStream)
	_, _ = io.WriteString(writer, stream)
}

// completionStream is one streamed text answer in the OpenAI wire format.
func (p *provider) completionStream() (string, error) {
	deltas := []map[string]string{
		{"role": "assistant", "content": p.answer},
		{},
	}

	var stream strings.Builder

	for index, delta := range deltas {
		var finish any
		if index == len(deltas)-1 {
			finish = finishReasonStop
		}

		encoded, err := json.Marshal(map[string]any{
			"id":        "chatcmpl-installer",
			fieldObject: "chat.completion.chunk",
			"created":   1,
			"model":     p.model,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		})
		if err != nil {
			return "", ctxerrors.Wrap(err, "encode a completion chunk")
		}

		stream.WriteString("data: " + string(encoded) + "\n\n")
	}

	stream.WriteString(streamDone)

	return stream.String(), nil
}
