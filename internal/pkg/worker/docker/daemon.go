package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/worker"
)

const (
	// daemonHost is the host name in every request URL. The connection is a
	// Unix socket, so the name is only there to make a valid URL.
	daemonHost = "http://docker"

	// engineAPIVersion pins the Engine API this client speaks. Pinning keeps a
	// daemon upgrade from silently changing the request shape underneath a
	// running controller.
	engineAPIVersion = "v1.43"

	// stopTimeoutSeconds is how long the daemon waits for the worker to exit
	// before killing it.
	stopTimeoutSeconds = 10

	// imageDigestSeparator splits a reference from its digest.
	imageDigestSeparator = "@"

	// imageTagSeparator introduces a tag.
	imageTagSeparator = ":"

	// daemonRequestTimeout bounds one ordinary daemon call. Attach is excluded
	// because it holds a connection open by design.
	daemonRequestTimeout = 30 * time.Second

	noNewPrivilegesSecurityOpt = "no-new-privileges:true"
)

// SocketGID reports the group that owns the Docker socket.
//
// A host-like profile mounts that socket into the worker, and the worker
// account needs this group to use it. Zero means the group could not be read,
// which refuses such a profile rather than launching a worker that cannot use
// the socket it was given.
func SocketGID(socketPath string) int {
	info, err := os.Stat(socketPath)
	if err != nil {
		return 0
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}

	return int(stat.Gid)
}

// daemonClient talks to the Docker Engine API over the controller's own Unix
// socket.
//
// It speaks the HTTP API directly rather than through a vendored SDK, because
// the five calls a worker launch needs are a small, stable part of that API and
// the alternative is a large dependency for them.
type daemonClient struct {
	socketPath string
	http       *http.Client

	// streamHTTP carries responses that stay open, which is the container log
	// follow. The ordinary client's timeout bounds one call and would cut a
	// worker's log stream off partway through its life.
	streamHTTP *http.Client
}

// NewDaemonClient opens a client for the controller's own Docker socket.
//
//nolint:ireturn // Callers hold the Client seam, not this implementation.
func NewDaemonClient(socketPath string) (Client, error) {
	if socketPath == "" {
		return nil, ctxerrors.Wrap(
			worker.ErrDockerAuthorityUnavailable,
			"no Docker socket path is configured",
		)
	}

	dialer := &net.Dialer{}
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, "unix", socketPath)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "dial the Docker socket")
		}

		return conn, nil
	}

	return &daemonClient{
		socketPath: socketPath,
		http: &http.Client{
			Timeout:   daemonRequestTimeout,
			Transport: &http.Transport{DialContext: dial},
		},
		streamHTTP: &http.Client{
			Transport: &http.Transport{DialContext: dial},
		},
	}, nil
}

// createBody is the Engine API container create payload.
//
// The field names are the daemon's own, so they stay PascalCase rather than
// following this project's JSON convention. Renaming them would simply not
// reach Docker.
//
//nolint:tagliatelle // The Docker Engine API defines these names.
type createBody struct {
	Image           string            `json:"Image"`
	Entrypoint      []string          `json:"Entrypoint,omitempty"`
	Cmd             []string          `json:"Cmd"`
	Env             []string          `json:"Env"`
	User            string            `json:"User"`
	WorkingDir      string            `json:"WorkingDir"`
	Labels          map[string]string `json:"Labels"`
	OpenStdin       bool              `json:"OpenStdin"`
	StdinOnce       bool              `json:"StdinOnce"`
	AttachStdin     bool              `json:"AttachStdin"`
	NetworkDisabled bool              `json:"NetworkDisabled"`
	HostConfig      createHostConfig  `json:"HostConfig"`
}

//nolint:tagliatelle // The Docker Engine API defines these names.
type createHostConfig struct {
	Binds       []string `json:"Binds"`
	GroupAdd    []string `json:"GroupAdd"`
	SecurityOpt []string `json:"SecurityOpt"`
	AutoRemove  bool     `json:"AutoRemove"`
}

//nolint:tagliatelle // The Docker Engine API defines these names.
type createResponse struct {
	ID string `json:"Id"`
}

//nolint:tagliatelle // The Docker Engine API defines these names.
type inspectResponse struct {
	ID string `json:"Id"`

	// Image is the daemon's local content ID for the running image, not a
	// repository digest. Recording it as a digest would name an identity no
	// registry can resolve, so it is carried as an ID and nothing else.
	Image string `json:"Image"`

	State struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

//nolint:tagliatelle // The Docker Engine API defines these names.
type imageInspectResponse struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
}

// CreateContainer creates the worker container without starting it.
func (c *daemonClient) CreateContainer(
	ctx context.Context,
	request CreateRequest,
) (string, error) {
	body := createBody{
		Image:           request.Image,
		Entrypoint:      request.Entrypoint,
		Cmd:             request.Command,
		Env:             environmentList(request.Env),
		User:            request.User,
		WorkingDir:      request.WorkingDirectory,
		Labels:          request.Labels,
		OpenStdin:       true,
		StdinOnce:       true,
		AttachStdin:     true,
		NetworkDisabled: request.NetworkDisabled,
		HostConfig: createHostConfig{
			Binds:    bindList(request.Mounts),
			GroupAdd: request.Groups,
		},
	}

	if request.NoNewPrivileges {
		body.HostConfig.SecurityOpt = []string{noNewPrivilegesSecurityOpt}
	}

	created := createResponse{}
	if err := c.call(
		ctx,
		http.MethodPost,
		"/containers/create?name="+url.QueryEscape(request.Name),
		body,
		&created,
	); err != nil {
		return "", err
	}

	if created.ID == "" {
		return "", ctxerrors.Wrap(
			commerr.ErrParseFailed,
			"the daemon returned no container ID",
		)
	}

	return created.ID, nil
}

// PullImage fetches one reference into the daemon's local store.
//
// The pull streams progress and answers 200 before it is finished, so a failure
// arrives as an error object inside the stream rather than as a status. The
// body is read to the end and scanned, because stopping early would leave a
// half-pulled image behind and report success.
func (c *daemonClient) PullImage(ctx context.Context, reference string) error {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		daemonHost+"/"+engineAPIVersion+"/images/create?"+pullQuery(reference),
		http.NoBody,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "build the image pull request")
	}

	// The stream client has no request timeout: a pull takes as long as the
	// image is large, and the ordinary client bounds one short call.
	response, err := c.streamHTTP.Do(request)
	if err != nil {
		return ctxerrors.Wrap(err, "pull the worker image")
	}

	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			ctxscope.GetLogger(ctx).Debug(
				"closing the image pull stream failed",
				"err", closeErr,
			)
		}
	}()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return ctxerrors.Wrap(err, "read the image pull progress")
	}

	if err := assertSuccess(response.StatusCode, body); err != nil {
		return ctxerrors.Wrapf(err, "pull worker image %q", reference)
	}

	return assertPullSucceeded(reference, body)
}

// pullQuery splits a reference into the parameters the pull endpoint takes.
//
// A digest goes in the tag parameter exactly as a tag does, which is what the
// daemon expects for a pull by digest.
func pullQuery(reference string) string {
	name := reference
	tag := ""

	if repository, digest, found := strings.Cut(
		reference,
		imageDigestSeparator,
	); found {
		name = repository
		tag = imageDigestSeparator + digest
	} else if repository, named, found := cutImageTag(reference); found {
		name = repository
		tag = named
	}

	query := url.Values{}
	query.Set("fromImage", name)

	if tag != "" {
		query.Set("tag", strings.TrimPrefix(tag, imageDigestSeparator))
	}

	return query.Encode()
}

// cutImageTag splits a trailing tag, leaving a registry host's port alone. That
// colon is always before a slash, a tag's never is.
func cutImageTag(reference string) (string, string, bool) {
	index := strings.LastIndex(reference, imageTagSeparator)
	if index < 0 || strings.Contains(reference[index:], "/") {
		return reference, "", false
	}

	return reference[:index], reference[index+1:], true
}

// assertPullSucceeded reads the progress stream for the error the daemon
// reports without changing the status code.
func assertPullSucceeded(reference string, body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))

	for decoder.More() {
		message := pullMessage{}
		if err := decoder.Decode(&message); err != nil {
			return ctxerrors.Wrapf(
				commerr.ErrParseFailed,
				"decode the pull progress for %q",
				reference,
			)
		}

		if message.Error != "" {
			// The daemon's text can name a registry host and credentials
			// state, so the reference is reported and the message is not.
			return ctxerrors.Wrapf(
				commerr.ErrFetchFailed,
				"pull worker image %q",
				reference,
			)
		}
	}

	return nil
}

// pullMessage is the one field this client reads from the progress stream.
type pullMessage struct {
	Error string `json:"error"`
}

// StreamLogs follows the container's combined output for its whole life.
//
// It uses a client without a request timeout, because the ordinary one bounds a
// single call and this response stays open until the worker exits. The daemon
// frames stdout and stderr for a container created without a TTY, so the body
// is demultiplexed before the caller sees it.
func (c *daemonClient) StreamLogs(
	ctx context.Context,
	containerID string,
) (io.ReadCloser, error) {
	path := "/" + engineAPIVersion + "/containers/" + containerID +
		"/logs?follow=1&stdout=1&stderr=1"

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		daemonHost+path,
		http.NoBody,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build the container logs request")
	}

	response, err := c.streamHTTP.Do(request)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "follow the container logs")
	}

	if response.StatusCode != http.StatusOK {
		if closeErr := response.Body.Close(); closeErr != nil {
			return nil, ctxerrors.Wrap(
				closeErr,
				"close the refused container logs response",
			)
		}

		return nil, ctxerrors.Wrapf(
			commerr.ErrFetchFailed,
			"container logs responded %d",
			response.StatusCode,
		)
	}

	return newLogStream(response.Body), nil
}

// logStream demultiplexes the daemon's framing while keeping the response body
// closable, so the caller stops the follow by closing what it was handed.
type logStream struct {
	io.Reader

	body io.ReadCloser
}

func newLogStream(body io.ReadCloser) *logStream {
	return &logStream{Reader: newLogDemultiplexer(body), body: body}
}

func (s *logStream) Close() error {
	if err := s.body.Close(); err != nil {
		return ctxerrors.Wrap(err, "close the container log stream")
	}

	return nil
}

// AttachStdin hands the launch document to the created container.
//
// The attach endpoint hijacks the connection, so this writes the request by
// hand rather than through the HTTP client. Closing the write side is what
// gives the worker an EOF on stdin, which is how it knows the document ended.
func (c *daemonClient) AttachStdin(
	ctx context.Context,
	containerID string,
	document []byte,
) error {
	dialer := &net.Dialer{}

	conn, err := dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return ctxerrors.Wrap(err, "dial the Docker socket for attach")
	}

	defer func() {
		if err := conn.Close(); err != nil {
			return
		}
	}()

	path := "/" + engineAPIVersion + "/containers/" + containerID +
		"/attach?stream=1&stdin=1"

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		daemonHost+path,
		http.NoBody,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "build the container attach request")
	}

	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "tcp")

	if err := openAttachStream(conn, request); err != nil {
		return err
	}

	if _, err := conn.Write(document); err != nil {
		return ctxerrors.Wrap(err, "write the worker launch document")
	}

	// The worker reads stdin until EOF, so the write side closes here rather
	// than when the whole connection does.
	closer, hasCloseWrite := conn.(interface{ CloseWrite() error })
	if !hasCloseWrite {
		return nil
	}

	if err := closer.CloseWrite(); err != nil {
		return ctxerrors.Wrap(err, "close the attach write side")
	}

	return nil
}

// openAttachStream sends the hijack request and checks the daemon accepted it.
func openAttachStream(conn net.Conn, request *http.Request) error {
	if err := request.Write(conn); err != nil {
		return ctxerrors.Wrap(err, "write the container attach request")
	}

	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		return ctxerrors.Wrap(err, "read the container attach response")
	}

	if response.StatusCode == http.StatusSwitchingProtocols {
		return nil
	}

	if err := response.Body.Close(); err != nil {
		return ctxerrors.Wrap(
			err,
			"close the rejected container attach response",
		)
	}

	return assertAttachStreamAccepted(response.StatusCode)
}

// assertAttachStreamAccepted checks the successful response shape for a
// hijacked Docker attach. A 101 response leaves conn open as the raw stdin
// stream. Closing response.Body here would close that stream before the
// launcher could send the worker launch document.
func assertAttachStreamAccepted(status int) error {
	if status == http.StatusSwitchingProtocols {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrExecFailed,
		"the Docker daemon did not upgrade the attach stream, status %d",
		status,
	)
}

func (c *daemonClient) StartContainer(
	ctx context.Context,
	containerID string,
) error {
	return c.call(
		ctx,
		http.MethodPost,
		"/containers/"+containerID+"/start",
		nil,
		nil,
	)
}

func (c *daemonClient) StopContainer(
	ctx context.Context,
	containerID string,
) error {
	return c.call(
		ctx,
		http.MethodPost,
		"/containers/"+containerID+"/stop?t="+
			strconv.Itoa(stopTimeoutSeconds),
		nil,
		nil,
	)
}

func (c *daemonClient) RemoveContainer(
	ctx context.Context,
	containerID string,
) error {
	return c.call(
		ctx,
		http.MethodDelete,
		"/containers/"+containerID,
		nil,
		nil,
	)
}

func (c *daemonClient) InspectContainer(
	ctx context.Context,
	containerID string,
) (ContainerState, error) {
	inspected := inspectResponse{}
	if err := c.call(
		ctx,
		http.MethodGet,
		"/containers/"+containerID+"/json",
		nil,
		&inspected,
	); err != nil {
		return ContainerState{}, err
	}

	return ContainerState{
		ID:       inspected.ID,
		Running:  inspected.State.Running,
		ExitCode: inspected.State.ExitCode,
		ImageID:  inspected.Image,
		Labels:   inspected.Config.Labels,
	}, nil
}

// InspectImage reports one image's local ID and its repository digests.
func (c *daemonClient) InspectImage(
	ctx context.Context,
	reference string,
) (ImageIdentity, error) {
	inspected := imageInspectResponse{}
	if err := c.call(
		ctx,
		http.MethodGet,
		"/images/"+url.PathEscape(reference)+"/json",
		nil,
		&inspected,
	); err != nil {
		return ImageIdentity{}, err
	}

	return ImageIdentity(inspected), nil
}

// newRequest builds one Engine API request, encoding a body when there is one.
func (c *daemonClient) newRequest(
	ctx context.Context,
	method string,
	path string,
	body any,
) (*http.Request, error) {
	var payload io.Reader = http.NoBody

	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "encode the daemon request")
		}

		payload = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(
		ctx,
		method,
		daemonHost+"/"+engineAPIVersion+path,
		payload,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build the daemon request")
	}

	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	return request, nil
}

// call performs one ordinary Engine API request.
func (c *daemonClient) call(
	ctx context.Context,
	method string,
	path string,
	body any,
	result any,
) error {
	request, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}

	response, err := c.http.Do(request)
	if err != nil {
		return ctxerrors.Wrap(err, "call the Docker daemon")
	}

	defer func() {
		if err := response.Body.Close(); err != nil {
			return
		}
	}()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return ctxerrors.Wrap(err, "read the daemon response")
	}

	if err := assertSuccess(response.StatusCode, raw); err != nil {
		return err
	}

	if result == nil {
		return nil
	}

	if err := json.Unmarshal(raw, result); err != nil {
		return ctxerrors.Wrap(
			commerr.ErrParseFailed,
			"decode the daemon response",
		)
	}

	return nil
}

// assertSuccess turns a daemon status into an error, never echoing the body,
// which can carry configuration detail an operator did not ask to see in logs.
func assertSuccess(status int, body []byte) error {
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return nil
	}

	if status == http.StatusNotFound {
		return ctxerrors.Wrapf(
			commerr.ErrNotFound,
			"the Docker daemon answered %d",
			status,
		)
	}

	if status == http.StatusForbidden || status == http.StatusUnauthorized {
		return ctxerrors.Wrapf(
			commerr.ErrPermissionDenied,
			"the Docker daemon answered %d",
			status,
		)
	}

	_ = body // The daemon's message is deliberately not logged.

	return ctxerrors.Wrapf(
		commerr.ErrExecFailed,
		"the Docker daemon answered %d",
		status,
	)
}

// environmentList renders the environment as the daemon expects it.
func environmentList(environment map[string]string) []string {
	rendered := make([]string, 0, len(environment))
	for key, value := range environment {
		rendered = append(rendered, key+"="+value)
	}

	return rendered
}

// bindList renders mounts as daemon bind strings with literal paths.
func bindList(mounts []worker.Mount) []string {
	binds := make([]string, 0, len(mounts))

	for _, mount := range mounts {
		bind := mount.Source + ":" + mount.Target
		if mount.ReadOnly {
			bind += ":ro"
		}

		binds = append(binds, bind)
	}

	return binds
}
