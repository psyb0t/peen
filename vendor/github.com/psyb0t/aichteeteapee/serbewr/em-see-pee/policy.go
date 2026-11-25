package emseepee

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/psyb0t/aichteeteapee"
)

const (
	// RFC 1035 limits for a DNS name and one of its labels.
	maxHostnameBytes = 253
	maxLabelBytes    = 63
	maxOriginBytes   = 2048
	maxTCPPort       = 65535
)

// hostAllowed reports whether a request authority (Host, with an optional
// port) names a host in hosts. Any valid port matches.
func hostAllowed(hosts map[string]struct{}, authority string) bool {
	host := authority
	if parsed, port, err := net.SplitHostPort(authority); err == nil {
		if !validPort(port) {
			return false
		}

		host = parsed
	}

	normalized, valid := normalizeHost(host)
	if !valid {
		return false
	}

	_, allowed := hosts[normalized]

	return allowed
}

// normalizeHost lowercases a DNS name, or canonicalizes an IP address with
// optional brackets. It rejects ports, IPv6 zones and invalid labels.
func normalizeHost(value string) (string, bool) {
	if value == "" || len(value) > maxHostnameBytes {
		return "", false
	}

	host := strings.ToLower(value)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}

	if address, err := netip.ParseAddr(host); err == nil {
		return address.String(), address.Zone() == ""
	}

	for label := range strings.SplitSeq(host, ".") {
		if !validLabel(label) {
			return "", false
		}
	}

	return host, true
}

// loopbackHost reports whether a normalized host always means this machine.
func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}

	address, err := netip.ParseAddr(host)

	return err == nil && address.IsLoopback()
}

func validLabel(label string) bool {
	if label == "" || len(label) > maxLabelBytes ||
		strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}

	for _, character := range label {
		if !hostnameCharacter(character) {
			return false
		}
	}

	return true
}

func hostnameCharacter(character rune) bool {
	return character == '-' || (character >= 'a' && character <= 'z') ||
		(character >= '0' && character <= '9')
}

// normalizeOrigin returns the canonical scheme://host[:port] form of a bare
// http(s) origin: lowercase scheme and host, and the port kept as written.
func normalizeOrigin(value string) (string, bool) {
	if len(value) > maxOriginBytes || strings.ContainsAny(value, "?#") {
		return "", false
	}

	parsed, err := url.Parse(value)
	if err != nil || !bareOriginURL(parsed) {
		return "", false
	}

	authority, valid := originAuthority(parsed)
	if !valid {
		return "", false
	}

	return parsed.Scheme + "://" + authority, true
}

// originAuthority returns the normalized host[:port] of an origin URL. A
// non-canonical IP literal or a stray character in the authority changes
// the text, so such an origin is refused.
func originAuthority(parsed *url.URL) (string, bool) {
	host, valid := normalizeHost(parsed.Hostname())
	if !valid || (parsed.Port() != "" && !validPort(parsed.Port())) {
		return "", false
	}

	authority := host
	if strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}

	if parsed.Port() != "" {
		authority = net.JoinHostPort(host, parsed.Port())
	}

	return authority, strings.EqualFold(parsed.Host, authority)
}

func bareOriginURL(parsed *url.URL) bool {
	if parsed.Scheme != aichteeteapee.SchemeHTTP &&
		parsed.Scheme != aichteeteapee.SchemeHTTPS {
		return false
	}

	return parsed.User == nil && parsed.Opaque == "" && parsed.Path == "" &&
		parsed.RawQuery == "" && parsed.Fragment == "" && !parsed.ForceQuery
}

func validPort(value string) bool {
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}

	port, err := strconv.Atoi(value)

	return err == nil && port > 0 && port <= maxTCPPort
}
