//go:build installer

package installer_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

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
		return descriptor{}, ctxerrors.Wrapf(errRegistryNotReady, "manifest %s answered %d: %s", tag, response.StatusCode, body)
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
