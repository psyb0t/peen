//go:build installer

package installer_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/psyb0t/ctxerrors"
)

func (s *installerSuite) startProvider(ctx context.Context) error {
	provider, err := startMockProvider(ctx, s)
	s.provider = provider

	return err
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
