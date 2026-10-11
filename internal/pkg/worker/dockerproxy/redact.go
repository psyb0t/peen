package dockerproxy

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/psyb0t/ctxerrors"
)

const (
	// inspectConfigKey holds a container's or image's configuration.
	inspectConfigKey = "Config"

	// inspectContainerConfigKey is the configuration of the container an
	// image was committed from. Daemons before Engine API 1.45 still report
	// it, and its Env carries the same values as Config.Env.
	inspectContainerConfigKey = "ContainerConfig"

	// inspectEnvKey is the environment list inside a configuration.
	inspectEnvKey = "Env"

	environmentSeparator = "="

	jsonNull = "null"

	// encoderLineEnd is the newline json.Encoder writes after each value.
	encoderLineEnd = "\n"
)

// redactInspectEnv replaces the value of every environment variable in an
// inspect document, keeping each variable name.
//
// Only the environment lists change. Every other field is carried over as the
// daemon sent it, apart from key order and whitespace, so a client that reads
// inspect output for anything else sees the same document.
func redactInspectEnv(body []byte) ([]byte, error) {
	document := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, ctxerrors.Wrap(err, "decode the inspect document")
	}

	for _, key := range []string{
		inspectConfigKey,
		inspectContainerConfigKey,
	} {
		config, found := document[key]
		if !found || isJSONNull(config) {
			continue
		}

		redacted, err := redactConfigEnv(config)
		if err != nil {
			return nil, ctxerrors.Wrapf(err, "redact %s", key)
		}

		document[key] = redacted
	}

	return encodeJSON(document)
}

// redactConfigEnv redacts the Env list of one configuration object.
func redactConfigEnv(raw json.RawMessage) (json.RawMessage, error) {
	config := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, ctxerrors.Wrap(err, "decode the configuration")
	}

	envRaw, found := config[inspectEnvKey]
	if !found || isJSONNull(envRaw) {
		return raw, nil
	}

	environment := []string{}
	if err := json.Unmarshal(envRaw, &environment); err != nil {
		return nil, ctxerrors.Wrap(err, "decode the environment list")
	}

	for index, entry := range environment {
		environment[index] = redactEnvironmentEntry(entry)
	}

	encodedEnvironment, err := encodeJSON(environment)
	if err != nil {
		return nil, err
	}

	config[inspectEnvKey] = encodedEnvironment

	return encodeJSON(config)
}

// redactEnvironmentEntry turns NAME=value into NAME=<redacted>. An entry
// without a value names a variable and carries nothing to hide.
func redactEnvironmentEntry(entry string) string {
	name, _, hasValue := strings.Cut(entry, environmentSeparator)
	if !hasValue {
		return entry
	}

	return name + environmentSeparator + RedactedValue
}

// encodeJSON encodes without HTML escaping, so the redaction marker and any
// angle brackets the daemon sent read the same as before.
func encodeJSON(value any) ([]byte, error) {
	buffer := &bytes.Buffer{}

	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(value); err != nil {
		return nil, ctxerrors.Wrap(err, "encode the inspect document")
	}

	return bytes.TrimSuffix(buffer.Bytes(), []byte(encoderLineEnd)), nil
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == jsonNull
}
