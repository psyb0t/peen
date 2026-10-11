package dockerproxy

import (
	"net/http"
	"slices"
	"strings"

	"github.com/psyb0t/aichteeteapee"
)

const (
	reasonMethod      = "only GET and HEAD requests are allowed"
	reasonEncodedPath = "the path carries encoded characters"
	reasonUncleanPath = "the path is not in canonical form"
	reasonUpgrade     = "connection upgrades are not allowed"
	reasonEndpoint    = "the endpoint is not one of the read endpoints " +
		"it answers"

	connectionUpgradeToken  = "upgrade"
	headerValueSeparator    = ","
	pathSeparator           = "/"
	currentDirectorySegment = "."
	parentDirectorySegment  = ".."
	apiVersionPrefix        = "v"
	apiVersionSeparator     = "."
	apiVersionParts         = 2

	// Path segments of the read endpoints the proxy answers.
	segmentPing       = "_ping"
	segmentVersion    = "version"
	segmentInfo       = "info"
	segmentEvents     = "events"
	segmentContainers = "containers"
	segmentImages     = "images"
	segmentNetworks   = "networks"
	segmentVolumes    = "volumes"
	segmentSystem     = "system"
	segmentJSON       = "json"
	segmentStats      = "stats"
	segmentDiskUsage  = "df"

	// containerEndpointSegments is containers, one ID, and an endpoint.
	containerEndpointSegments = 3

	// pairSegments is a collection and one item, or a collection and its list.
	pairSegments = 2
)

// access is what the proxy does with a request path.
type access int

const (
	// accessRefused answers 403 and never reaches the daemon.
	accessRefused access = 0
	// accessForward passes the request and its response through unchanged.
	accessForward access = 1
	// accessRedactEnv passes the request through and redacts environment
	// values in the inspect document the daemon returns.
	accessRedactEnv access = 2
)

// refusalReason says why a request is refused, or returns an empty string for
// one the proxy forwards.
//
// The path checks run before the allowlist, so a path that only reaches an
// allowed endpoint after the daemon decodes or cleans it is refused rather than
// matched against what it looks like here.
func refusalReason(request *http.Request) string {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		return reasonMethod
	}

	if request.URL.RawPath != "" {
		return reasonEncodedPath
	}

	if isUncleanPath(request.URL.Path) {
		return reasonUncleanPath
	}

	if requestsUpgrade(request.Header) {
		return reasonUpgrade
	}

	if classify(request.URL.Path) == accessRefused {
		return reasonEndpoint
	}

	return ""
}

// isUncleanPath reports whether a path is not in canonical form: it holds a
// "." or ".." segment, which the daemon would resolve to a different endpoint
// than the one matched here, or an empty segment from "//" or a trailing
// slash. The leading slash's empty first segment is the one allowed, and "/"
// alone is clean.
func isUncleanPath(requestPath string) bool {
	if requestPath == pathSeparator {
		return false
	}

	segments := strings.Split(requestPath, pathSeparator)

	for index, segment := range segments {
		if segment == "" && index > 0 {
			return true
		}

		if segment == currentDirectorySegment ||
			segment == parentDirectorySegment {
			return true
		}
	}

	return false
}

// requestsUpgrade reports whether a request asks to switch protocols. The
// daemon serves attach and exec that way, and a hijacked connection would let
// the client write to the daemon past every check here.
func requestsUpgrade(header http.Header) bool {
	if header.Get(aichteeteapee.HeaderNameUpgrade) != "" {
		return true
	}

	for _, value := range header.Values(aichteeteapee.HeaderNameConnection) {
		for token := range strings.SplitSeq(value, headerValueSeparator) {
			if strings.EqualFold(
				strings.TrimSpace(token),
				connectionUpgradeToken,
			) {
				return true
			}
		}
	}

	return false
}

// classify matches a canonical request path against the read endpoints the
// proxy answers. Anything not listed is refused, so a daemon endpoint added
// later stays closed until it is added here on purpose.
func classify(requestPath string) access {
	segments := strings.Split(
		strings.TrimPrefix(requestPath, pathSeparator),
		pathSeparator,
	)

	if isAPIVersion(segments[0]) {
		segments = segments[1:]
	}

	if len(segments) == 0 || slices.Contains(segments, "") {
		return accessRefused
	}

	switch len(segments) {
	case 1:
		return classifyOne(segments[0])
	case pairSegments:
		return classifyPair(segments[0], segments[1])
	default:
		return classifyLong(segments)
	}
}

func classifyOne(segment string) access {
	switch segment {
	case segmentPing, segmentVersion, segmentInfo, segmentEvents,
		segmentNetworks, segmentVolumes:
		return accessForward
	default:
		return accessRefused
	}
}

func classifyPair(collection, item string) access {
	switch collection {
	case segmentSystem:
		if item == segmentDiskUsage {
			return accessForward
		}
	case segmentContainers, segmentImages:
		if item == segmentJSON {
			return accessForward
		}
	case segmentNetworks, segmentVolumes:
		return accessForward
	}

	return accessRefused
}

// classifyLong handles container inspect and stats, which take one ID
// segment, and image inspect, whose name may hold slashes.
func classifyLong(segments []string) access {
	last := segments[len(segments)-1]

	switch segments[0] {
	case segmentContainers:
		if len(segments) != containerEndpointSegments {
			return accessRefused
		}

		switch last {
		case segmentJSON:
			return accessRedactEnv
		case segmentStats:
			return accessForward
		}
	case segmentImages:
		if last == segmentJSON {
			return accessRedactEnv
		}
	}

	return accessRefused
}

// isAPIVersion reports whether a path segment is an Engine API version
// prefix such as v1.43.
func isAPIVersion(segment string) bool {
	version, found := strings.CutPrefix(segment, apiVersionPrefix)
	if !found {
		return false
	}

	parts := strings.Split(version, apiVersionSeparator)
	if len(parts) != apiVersionParts {
		return false
	}

	for _, part := range parts {
		if !isDigits(part) {
			return false
		}
	}

	return true
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}

	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}

	return true
}
