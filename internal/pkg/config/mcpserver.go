package config

import (
	"maps"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

const (
	defaultMCPHosts = "localhost,127.0.0.1,[::1]"
	//nolint:lll // Exact origins mirror the default HTTP listener.
	defaultMCPOrigins       = "http://localhost:8080,http://127.0.0.1:8080,http://[::1]:8080"
	maximumMCPPolicyEntries = 128
	maximumMCPHostBytes     = 253
	maximumMCPLabelBytes    = 63
	maximumMCPOriginBytes   = 2048
	maximumTCPPort          = 65535
)

// MCPServerPolicy permits only explicitly configured HTTP authorities.
type MCPServerPolicy struct {
	hosts   map[string]struct{}
	origins map[string]struct{}
}

func (c Config) MCPServerPolicy() (MCPServerPolicy, error) {
	policy := MCPServerPolicy{
		hosts:   make(map[string]struct{}),
		origins: make(map[string]struct{}),
	}

	hosts := c.MCPServerAllowedHosts
	if hosts == "" {
		hosts = defaultMCPHosts
	}

	origins := c.MCPServerAllowedOrigins
	if origins == "" {
		origins = defaultMCPOrigins
	}

	if err := policy.addHosts(hosts); err != nil {
		return MCPServerPolicy{}, ctxerrors.Wrap(err,
			"PEEN_MCP_SERVER_ALLOWED_HOSTS is invalid")
	}

	if err := policy.addOrigins(origins); err != nil {
		return MCPServerPolicy{}, ctxerrors.Wrap(err,
			"PEEN_MCP_SERVER_ALLOWED_ORIGINS is invalid")
	}

	return policy, nil
}

func (p *MCPServerPolicy) addHosts(value string) error {
	entries := strings.Split(value, ",")
	if len(entries) > maximumMCPPolicyEntries {
		return ctxerrors.Wrap(ErrInvalidConfig, "too many MCP hosts")
	}

	for _, entry := range entries {
		normalized, valid := mcpHostname(strings.TrimSpace(entry))
		if !valid {
			return ctxerrors.Wrap(ErrInvalidConfig, "invalid MCP host")
		}

		p.hosts[normalized] = struct{}{}
	}

	return nil
}

func (p *MCPServerPolicy) addOrigins(value string) error {
	entries := strings.Split(value, ",")
	if len(entries) > maximumMCPPolicyEntries {
		return ctxerrors.Wrap(ErrInvalidConfig, "too many MCP origins")
	}

	for _, entry := range entries {
		origin, valid := mcpOrigin(strings.TrimSpace(entry))
		if !valid {
			return ctxerrors.Wrap(ErrInvalidConfig, "invalid MCP origin")
		}

		p.origins[origin] = struct{}{}
	}

	return nil
}

func (p MCPServerPolicy) AllowsHost(authority string) bool {
	host := authority
	if parsed, port, err := net.SplitHostPort(authority); err == nil {
		if !validMCPPort(port) {
			return false
		}

		host = parsed
	}

	normalized, valid := mcpHostname(host)
	if !valid {
		return false
	}

	_, allowed := p.hosts[normalized]

	return allowed
}

func (p MCPServerPolicy) AllowsOrigin(origin string) bool {
	normalized, valid := mcpOrigin(origin)
	if !valid {
		return false
	}

	_, allowed := p.origins[normalized]

	return allowed
}

// Hosts returns the allowed hosts in sorted order: lowercase DNS names and
// canonical IP addresses, without brackets or ports.
func (p MCPServerPolicy) Hosts() []string {
	return slices.Sorted(maps.Keys(p.hosts))
}

// Origins returns the allowed browser origins, normalized, in sorted order.
func (p MCPServerPolicy) Origins() []string {
	return slices.Sorted(maps.Keys(p.origins))
}

func mcpHostname(value string) (string, bool) {
	if value == "" || len(value) > maximumMCPHostBytes {
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
		if !validMCPLabel(label) {
			return "", false
		}
	}

	return host, true
}

func validMCPLabel(label string) bool {
	if label == "" || len(label) > maximumMCPLabelBytes ||
		strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}

	for _, character := range label {
		if !mcpHostnameCharacter(character) {
			return false
		}
	}

	return true
}

func mcpHostnameCharacter(character rune) bool {
	return character == '-' || (character >= 'a' && character <= 'z') ||
		(character >= '0' && character <= '9')
}

func mcpOrigin(value string) (string, bool) {
	if len(value) > maximumMCPOriginBytes || strings.ContainsAny(value, "?#") {
		return "", false
	}

	parsed, err := url.Parse(value)
	if err != nil || !validMCPOriginURL(parsed) {
		return "", false
	}

	host, valid := mcpOriginAuthority(parsed)
	if !valid || !strings.EqualFold(parsed.Host, host) {
		return "", false
	}

	return parsed.Scheme + "://" + host, true
}

func validMCPOriginURL(parsed *url.URL) bool {
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}

	return parsed.User == nil && parsed.Opaque == "" && parsed.Path == "" &&
		parsed.RawQuery == "" && parsed.Fragment == "" && !parsed.ForceQuery
}

func mcpOriginAuthority(parsed *url.URL) (string, bool) {
	host, valid := mcpHostname(parsed.Hostname())
	if !valid || (parsed.Port() != "" && !validMCPPort(parsed.Port())) {
		return "", false
	}

	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}

	if parsed.Port() != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), parsed.Port())
	}

	return host, true
}

func validMCPPort(value string) bool {
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}

	port, err := strconv.Atoi(value)

	return err == nil && port > 0 && port <= maximumTCPPort
}
