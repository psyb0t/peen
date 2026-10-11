//go:build installer

// Package installer_test runs install.sh and the peen command end to end.
//
// TestMain builds the image from this checkout once, pushes it to a local
// registry under release tags, and puts a token-checking proxy in front of that
// registry, so the wrapper's anonymous Bearer flow and Docker's own pull both
// run for real. It serves the wrapper source per release from file:// and the
// "latest release" redirect from a local server, and answers model calls from
// a mock OpenAI-compatible provider in a container on the default bridge, which
// controller containers, worker containers, and host binaries all reach.
//
// Containment rules:
//
//   - Every home, configuration, and workspace lives under the repository's
//     gitignored .testing/ directory. The DIND Make target mounts the
//     repository at its literal host path, so controller and worker containers
//     that bind those paths through the host daemon see the same bytes.
//   - Every container this suite starts has a name it generated, and teardown
//     removes containers only by that exact name or by the IDs of workers that
//     belong to sessions a test opened.
//   - Images are removed by the exact references this suite pushed or pulled.
//   - The tests run one at a time. They share release tags that upgrade and
//     uninstall remove from the local Docker image store, and each one starts
//     real controllers on fixed ports.
package installer_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/psyb0t/ctxerrors"
)

const (
	suiteSetupTimeout    = 25 * time.Minute
	suiteTeardownTimeout = 2 * time.Minute
	readinessTimeout     = 60 * time.Second
	readinessInterval    = 200 * time.Millisecond

	releaseA     = "v0.20.0"
	releaseB     = "v0.20.1"
	releaseIndex = "v0.20.2"
	releaseOld   = "v0.19.1"
	rollingTag   = "latest"
	rollingRef   = "main"
	repository   = "psyb0t/peen"

	// registryImage is pinned by digest so the suite never runs a moved tag.
	registryImage         = "registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
	registryContainerPort = "5000/tcp"
	registryToken         = "installer-test-registry-token"
	registryService       = "installer-test"
	curlUserAgentPrefix   = "curl/"

	mediaTypeOCIIndex     = "application/vnd.oci.image.index.v1+json"
	mediaTypeOCIManifest  = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIConfig    = "application/vnd.oci.image.config.v1+json"
	mediaTypeOCILayerGzip = "application/vnd.oci.image.layer.v1.tar+gzip"
	mediaTypeDockerV2     = "application/vnd.docker.distribution.manifest.v2+json"

	providerName    = "itest"
	providerModel   = "test-model"
	providerKeyName = "ITEST_PROVIDER_CRED"
	providerKey     = "installer-test-provider-key"
	providerAnswer  = "installer turn complete"
	// mockProviderPort is the mock provider's port inside its own container.
	mockProviderPort = "8080"

	releaseMarkerPrefix = "# installer-test-release "
	testRunDirPrefix    = "installer-run-"
	containerNamePrefix = "peen-itest-"
	dockerSocketPath    = "/var/run/docker.sock"
)

var errRegistryNotReady = errors.New("the local registry did not answer")

// suite is the infrastructure TestMain starts once.
//
//nolint:gochecknoglobals // TestMain owns it and every test reads it.
var suite *installerSuite

type installerSuite struct {
	repoRoot          string
	runID             string
	runDir            string
	sourceURL         string
	image             string
	registryContainer string
	registryAddress   string
	proxy             *httptest.Server
	proxyHost         string
	latest            *httptest.Server
	latestTag         atomic.Value
	provider          *mockProvider
	uid               string
	gid               string
	socketGroup       string
	installScript     []byte
	curlTokenRequests atomic.Int64
	pulledReferences  sync.Map
}

func setupSuite(ctx context.Context) (*installerSuite, error) {
	s := &installerSuite{
		uid: strconv.Itoa(os.Getuid()),
		gid: strconv.Itoa(os.Getgid()),
	}
	s.latestTag.Store(releaseA)

	if err := s.prepareDirectories(); err != nil {
		return s, err
	}

	steps := []func(context.Context) error{
		s.readSocketGroup,
		s.buildImage,
		s.startRegistry,
		s.pushReleases,
		s.pushIndexRelease,
		s.startProxy,
		s.startProvider,
		s.writeSources,
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return s, err
		}
	}
	s.startLatestServer()

	return s, nil
}

func (s *installerSuite) prepareDirectories() error {
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	s.repoRoot = root
	s.runID = randomHex(4)
	s.runDir = filepath.Join(root, ".testing", testRunDirPrefix+s.runID)
	if err := os.MkdirAll(s.runDir, 0o700); err != nil {
		return ctxerrors.Wrap(err, "create the run directory")
	}
	s.installScript, err = os.ReadFile(filepath.Join(root, "install.sh"))
	if err != nil {
		return ctxerrors.Wrap(err, "read install.sh")
	}

	return nil
}

func (s *installerSuite) readSocketGroup(_ context.Context) error {
	info, err := os.Stat(dockerSocketPath)
	if err != nil {
		return ctxerrors.Wrap(err, "stat the Docker socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ctxerrors.New("the Docker socket has no Unix ownership")
	}
	s.socketGroup = strconv.FormatUint(uint64(stat.Gid), 10)

	return nil
}

func (s *installerSuite) buildImage(ctx context.Context) error {
	s.image = containerNamePrefix + "image-" + s.runID
	//nolint:gosec // Fixed arguments plus a tag this suite generated.
	command := exec.CommandContext(
		ctx,
		"docker", "build", "--quiet", "--tag", s.image, "--file", "Dockerfile", ".",
	)
	command.Dir = s.repoRoot
	command.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	output, err := command.CombinedOutput()
	if err != nil {
		s.image = ""

		return ctxerrors.Wrapf(err, "build the image: %s", output)
	}

	return nil
}

func (s *installerSuite) startRegistry(ctx context.Context) error {
	name := containerNamePrefix + "registry-" + s.runID
	if _, err := docker(ctx, "run", "-d", "--name", name, "-p", "127.0.0.1::5000", registryImage); err != nil {
		return err
	}
	s.registryContainer = name

	ports, err := docker(ctx, "port", name, registryContainerPort)
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(ports, "\n") {
		if strings.HasPrefix(line, "127.0.0.1:") {
			s.registryAddress = strings.TrimSpace(line)

			break
		}
	}
	if s.registryAddress == "" {
		return ctxerrors.Wrapf(errRegistryNotReady, "no loopback port in %q", ports)
	}

	return waitForStatus(ctx, "http://"+s.registryAddress+"/v2/", http.StatusOK)
}

func (s *installerSuite) pushReleases(ctx context.Context) error {
	for _, tag := range []string{releaseA, releaseB, rollingTag} {
		reference := s.registryAddress + "/" + repository + ":" + tag
		if _, err := docker(ctx, "tag", s.image, reference); err != nil {
			return err
		}
		if _, err := docker(ctx, "push", reference); err != nil {
			return err
		}
		if _, err := docker(ctx, "image", "rm", reference); err != nil {
			return err
		}
	}

	return nil
}

type descriptor struct {
	MediaType string    `json:"mediaType"`
	Digest    string    `json:"digest"`
	Size      int       `json:"size"`
	Platform  *platform `json:"platform,omitempty"`
}

type platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}

// pushIndexRelease publishes releaseIndex as a multi-platform OCI index shaped
// like a buildx release: an attestation entry with an unknown platform first,
// a decoy image for the other architecture, then the real image for this one.
// The decoy holds no app/app, so a wrong pick fails the host download.
func (s *installerSuite) pushIndexRelease(ctx context.Context) error {
	realDescriptor, err := s.manifestDescriptor(ctx, releaseA)
	if err != nil {
		return err
	}
	decoy, err := s.pushDecoyImage(ctx)
	if err != nil {
		return err
	}

	otherArchitecture := "arm64"
	if runtime.GOARCH == "arm64" {
		otherArchitecture = "amd64"
	}
	attestation := realDescriptor
	attestation.Platform = &platform{Architecture: "unknown", OS: "unknown"}
	decoy.Platform = &platform{Architecture: otherArchitecture, OS: "linux"}
	realDescriptor.Platform = &platform{Architecture: runtime.GOARCH, OS: "linux"}

	index, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     mediaTypeOCIIndex,
		"manifests":     []descriptor{attestation, decoy, realDescriptor},
	})
	if err != nil {
		return ctxerrors.Wrap(err, "encode the index")
	}

	return s.putManifest(ctx, releaseIndex, mediaTypeOCIIndex, index)
}

func (s *installerSuite) manifestDescriptor(ctx context.Context, tag string) (descriptor, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://"+s.registryAddress+"/v2/"+repository+"/manifests/"+tag,
		nil,
	)
	if err != nil {
		return descriptor{}, ctxerrors.Wrap(err, "build the manifest request")
	}
	request.Header.Set("Accept", mediaTypeDockerV2+", "+mediaTypeOCIManifest)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return descriptor{}, ctxerrors.Wrap(err, "fetch the manifest")
	}
	defer closeBody(response)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return descriptor{}, ctxerrors.Wrap(err, "read the manifest")
	}
	if response.StatusCode != http.StatusOK {
		return descriptor{}, ctxerrors.Wrapf(errRegistryNotReady, "manifest %s answered %d", tag, response.StatusCode)
	}

	return descriptor{
		MediaType: response.Header.Get("Content-Type"),
		Digest:    digestOf(body),
		Size:      len(body),
	}, nil
}

func (s *installerSuite) pushDecoyImage(ctx context.Context) (descriptor, error) {
	layer, err := gzipTar("decoy.txt", []byte("this image has no binary\n"))
	if err != nil {
		return descriptor{}, err
	}
	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	for _, blob := range [][]byte{layer, config} {
		if err := s.putBlob(ctx, blob); err != nil {
			return descriptor{}, err
		}
	}
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     mediaTypeOCIManifest,
		"config": descriptor{
			MediaType: mediaTypeOCIConfig,
			Digest:    digestOf(config),
			Size:      len(config),
		},
		"layers": []descriptor{{
			MediaType: mediaTypeOCILayerGzip,
			Digest:    digestOf(layer),
			Size:      len(layer),
		}},
	})
	if err != nil {
		return descriptor{}, ctxerrors.Wrap(err, "encode the decoy manifest")
	}
	if err := s.putManifest(ctx, digestOf(manifest), mediaTypeOCIManifest, manifest); err != nil {
		return descriptor{}, err
	}

	return descriptor{
		MediaType: mediaTypeOCIManifest,
		Digest:    digestOf(manifest),
		Size:      len(manifest),
	}, nil
}

func (s *installerSuite) putBlob(ctx context.Context, blob []byte) error {
	headers, err := s.registryRequest(ctx, http.MethodPost, "/v2/"+repository+"/blobs/uploads/", "", nil)
	if err != nil {
		return err
	}
	location, err := url.Parse(headers.Get("Location"))
	if err != nil {
		return ctxerrors.Wrap(err, "parse the upload location")
	}
	query := location.Query()
	query.Set("digest", digestOf(blob))
	location.RawQuery = query.Encode()
	path := location.Path + "?" + location.RawQuery
	_, err = s.registryRequest(ctx, http.MethodPut, path, "application/octet-stream", blob)

	return err
}

func (s *installerSuite) putManifest(ctx context.Context, reference, mediaType string, body []byte) error {
	_, err := s.registryRequest(ctx, http.MethodPut, "/v2/"+repository+"/manifests/"+reference, mediaType, body)

	return err
}

func (s *installerSuite) registryRequest(
	ctx context.Context,
	method, path, contentType string,
	body []byte,
) (http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, method, "http://"+s.registryAddress+path, bytes.NewReader(body))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build the registry request")
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, ctxerrors.Wrapf(err, "%s %s", method, path)
	}
	defer closeBody(response)
	text, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read the registry response")
	}
	if response.StatusCode >= http.StatusBadRequest {
		return nil, ctxerrors.Wrapf(errRegistryNotReady, "%s %s answered %d: %s", method, path, response.StatusCode, text)
	}

	return response.Header, nil
}

// startProxy fronts the registry with the Bearer challenge Docker Hub uses.
// Blob downloads redirect to the registry's own port, as Docker Hub redirects
// them to a CDN.
func (s *installerSuite) startProxy(_ context.Context) error {
	target, err := url.Parse("http://" + s.registryAddress)
	if err != nil {
		return ctxerrors.Wrap(err, "parse the registry address")
	}
	reverse := httputil.NewSingleHostReverseProxy(target)
	s.proxy = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			if strings.HasPrefix(request.UserAgent(), curlUserAgentPrefix) {
				s.curlTokenRequests.Add(1)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{"token":%q}`, registryToken)

			return
		}
		if request.Header.Get("Authorization") != "Bearer "+registryToken {
			writer.Header().Set("WWW-Authenticate", fmt.Sprintf(
				`Bearer realm="%s/token",service=%q,scope="repository:%s:pull"`,
				s.proxy.URL, registryService, repository,
			))
			writer.WriteHeader(http.StatusUnauthorized)

			return
		}
		if request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/blobs/") {
			// Docker Hub answers a blob with a redirect to another host, and
			// the installer must follow it without forwarding its token. The
			// target host is the suite's own registry, never request input.
			//nolint:gosec // See above.
			// nosemgrep: go.lang.security.injection.open-redirect.open-redirect
			http.Redirect(writer, request, "http://"+s.registryAddress+request.URL.Path, http.StatusTemporaryRedirect)

			return
		}
		request.Header.Del("Authorization")
		reverse.ServeHTTP(writer, request)
	}))
	s.proxyHost = strings.TrimPrefix(s.proxy.URL, "http://")

	return nil
}

func (s *installerSuite) startProvider(ctx context.Context) error {
	provider, err := startMockProvider(ctx, s)
	s.provider = provider

	return err
}

// writeSources lays out PEEN_INSTALL_SOURCE_URL as <ref>/scripts/peen.sh. Every
// copy but the first release carries a marker line, so a test can tell which
// one a refresh installed.
func (s *installerSuite) writeSources(_ context.Context) error {
	wrapper, err := os.ReadFile(filepath.Join(s.repoRoot, "scripts", "peen.sh"))
	if err != nil {
		return ctxerrors.Wrap(err, "read scripts/peen.sh")
	}
	sources := filepath.Join(s.runDir, "src")
	for _, reference := range []string{releaseA, releaseB, releaseIndex, rollingRef} {
		content := wrapper
		if reference != releaseA {
			content = append(append([]byte{}, wrapper...), []byte(releaseMarkerPrefix+reference+"\n")...)
		}
		directory := filepath.Join(sources, reference, "scripts")
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return ctxerrors.Wrap(err, "create the source directory")
		}
		if err := os.WriteFile(filepath.Join(directory, "peen.sh"), content, 0o644); err != nil { //nolint:gosec // A served script must be readable.
			return ctxerrors.Wrap(err, "write the wrapper source")
		}
	}
	s.sourceURL = "file://" + sources

	return nil
}

// startLatestServer answers like github.com/<repo>/releases/latest: a redirect
// to /releases/tag/<tag>.
func (s *installerSuite) startLatestServer() {
	s.latest = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/releases/latest" {
			tag, _ := s.latestTag.Load().(string)
			http.Redirect(writer, request, "/releases/tag/"+tag, http.StatusFound)

			return
		}
		if strings.HasPrefix(request.URL.Path, "/releases/tag/") {
			_, _ = io.WriteString(writer, "release page\n")

			return
		}
		http.NotFound(writer, request)
	}))
}

func (s *installerSuite) latestURL() string {
	return s.latest.URL + "/releases/latest"
}

func (s *installerSuite) setLatest(tag string) func() {
	previous, _ := s.latestTag.Load().(string)
	s.latestTag.Store(tag)

	return func() { s.latestTag.Store(previous) }
}

// imageReference is the reference the wrapper pins for a tag, through the
// proxy, and it records the reference so teardown removes it.
func (s *installerSuite) imageReference(tag string) string {
	reference := s.proxyHost + "/" + repository + ":" + tag
	s.pulledReferences.Store(reference, true)

	return reference
}

func (s *installerSuite) teardown(ctx context.Context) error {
	var failures []string
	if s.proxy != nil {
		s.proxy.Close()
	}
	if s.latest != nil {
		s.latest.Close()
	}
	if s.provider != nil {
		if _, err := docker(ctx, "rm", "-f", s.provider.container); err != nil {
			failures = append(failures, err.Error())
		}
	}
	for _, tag := range []string{releaseA, releaseB, releaseIndex, rollingTag} {
		s.imageReference(tag)
	}
	s.pulledReferences.Range(func(key, _ any) bool {
		reference, _ := key.(string)
		removeImageIfPresent(ctx, reference, &failures)

		return true
	})
	if s.registryContainer != "" {
		if _, err := docker(ctx, "rm", "-f", "-v", s.registryContainer); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if s.image != "" {
		removeImageIfPresent(ctx, s.image, &failures)
	}
	if len(failures) == 0 {
		if err := os.RemoveAll(s.runDir); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return ctxerrors.New(strings.Join(failures, "; "))
	}

	return nil
}

func removeImageIfPresent(ctx context.Context, reference string, failures *[]string) {
	if _, err := docker(ctx, "image", "inspect", reference); err != nil {
		return
	}
	if _, err := docker(ctx, "image", "rm", reference); err != nil {
		*failures = append(*failures, err.Error())
	}
}

// mockProvider is the tests/installer/mockprovider program running in a
// container on the default bridge. Host processes and bridge containers both
// reach its container address, which a port on the host does not guarantee.
type mockProvider struct {
	container string
	url       string
	statsURL  string
}

func startMockProvider(ctx context.Context, s *installerSuite) (*mockProvider, error) {
	binary := filepath.Join(s.runDir, "mockprovider")
	build := exec.CommandContext(
		ctx,
		"go", "build", "-tags", "installer", "-o", binary, "./tests/installer/mockprovider",
	)
	build.Dir = s.repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		return nil, ctxerrors.Wrapf(err, "build the mock provider: %s", output)
	}

	provider := &mockProvider{container: containerNamePrefix + "provider-" + s.runID}
	_, err := docker(
		ctx,
		"run", "-d", "--name", provider.container,
		"--user", s.uid+":"+s.gid,
		"-v", binary+":"+binary+":ro",
		"--entrypoint", binary,
		s.image,
		"-listen", ":"+mockProviderPort,
		"-key", providerKey,
		"-model", providerModel,
		"-answer", providerAnswer,
	)
	if err != nil {
		return provider, err
	}
	address, err := docker(
		ctx,
		"container", "inspect", "-f", `{{(index .NetworkSettings.Networks "bridge").IPAddress}}`, provider.container,
	)
	if err != nil {
		return provider, err
	}
	base := "http://" + net.JoinHostPort(address, mockProviderPort)
	provider.url = base + "/v1"
	provider.statsURL = base + "/stats"

	return provider, waitForStatus(ctx, provider.statsURL, http.StatusOK)
}

// completions is how many completions the provider has served.
func (p *mockProvider) completions(ctx context.Context) (int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.statsURL, nil)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "build the stats request")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "fetch the provider stats")
	}
	defer closeBody(response)
	var stats struct {
		Completions int64 `json:"completions"`
	}
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil {
		return 0, ctxerrors.Wrap(err, "decode the provider stats")
	}

	return stats.Completions, nil
}

func docker(ctx context.Context, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "docker", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), ctxerrors.Wrapf(err, "docker %s: %s", strings.Join(arguments, " "), output)
	}

	return strings.TrimSpace(string(output)), nil
}

func waitForStatus(ctx context.Context, address string, want int) error {
	deadline := time.Now().Add(readinessTimeout)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return ctxerrors.Wrap(err, "build the readiness request")
		}
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			closeBody(response)
			if response.StatusCode == want {
				return nil
			}
		}
		time.Sleep(readinessInterval)
	}

	return ctxerrors.Wrapf(errRegistryNotReady, "%s did not answer %d", address, want)
}

func closeBody(response *http.Response) {
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)

	return "sha256:" + hex.EncodeToString(sum[:])
}

func gzipTar(name string, content []byte) ([]byte, error) {
	var buffer bytes.Buffer
	compressor := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressor)
	if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
		return nil, ctxerrors.Wrap(err, "write the tar header")
	}
	if _, err := archive.Write(content); err != nil {
		return nil, ctxerrors.Wrap(err, "write the tar member")
	}
	if err := archive.Close(); err != nil {
		return nil, ctxerrors.Wrap(err, "close the tar")
	}
	if err := compressor.Close(); err != nil {
		return nil, ctxerrors.Wrap(err, "close the gzip stream")
	}

	return buffer.Bytes(), nil
}

func randomHex(length int) string {
	buffer := make([]byte, (length+1)/2)
	_, _ = rand.Read(buffer)

	return hex.EncodeToString(buffer)[:length]
}

func repositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", ctxerrors.Wrap(err, "get the working directory")
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", ctxerrors.New("go.mod not found above the working directory")
		}
		directory = parent
	}
}
