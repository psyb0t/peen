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
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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
	// pushIndexRelease supplies the fixture's attestation and platform entries.
	//nolint:gosec // Fixed arguments plus a tag this suite generated.
	command := exec.CommandContext(
		ctx,
		"docker", "build", "--provenance=false", "--quiet", "--tag", s.image, "--file", "Dockerfile", ".",
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
