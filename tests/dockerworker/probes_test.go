//go:build dockerworker

package dockerworker_test

// probeScript is fixed text. Every path it touches arrives through the
// environment, so nothing from the test is interpolated into shell source.
//
// It runs after the Peen entrypoint has dropped privileges, records what it can
// observe, and writes one JSON object into the bind-mounted workspace. It does
// not use set -e, because most of the commands below are expected to fail and
// their failure is the result being recorded.
const probeScript = `
set -u

uid="$(id -u)"
gid="$(id -g)"
username="$(id -un)"

sudo_allowed=false
if sudo -n true >/dev/null 2>&1; then
    sudo_allowed=true
fi

docker_socket_present=false
if [ -e /var/run/docker.sock ]; then
    docker_socket_present=true
fi

config_readable=false
if [ -r "$PEEN_CONFIG_DIR/AGENTS.md" ]; then
    config_readable=true
fi

state_secret_visible=false
if [ -e "$PEEN_PROBE_STATE_SECRET" ]; then
    state_secret_visible=true
fi

foreign_socket_visible=false
if [ -e "$PEEN_PROBE_FOREIGN_SOCKET" ]; then
    foreign_socket_visible=true
fi

session_socket_visible=false
if [ -d "$PEEN_PROBE_SESSION_SOCKET_DIR" ]; then
    session_socket_visible=true
fi

socket_root_entries=0
if [ -d "$PEEN_PROBE_SOCKET_ROOT" ]; then
    socket_root_entries="$(ls -1 "$PEEN_PROBE_SOCKET_ROOT" 2>/dev/null | wc -l | tr -d ' ')"
fi

network_reachable=false
if timeout 5 bash -c 'exec 3<>/dev/tcp/1.1.1.1/443' >/dev/null 2>&1; then
    network_reachable=true
fi

config_writable=false
if touch "$PEEN_CONFIG_DIR/probe-write" >/dev/null 2>&1; then
    config_writable=true
fi

learned_readable=false
if [ -r "$PEEN_PROBE_LEARNED_RULE" ]; then
    learned_readable=true
fi

learned_writable=false
if touch "$PEEN_PROBE_LEARNED_DIR/probe-write" >/dev/null 2>&1; then
    learned_writable=true
fi

touch "$PEEN_PROBE_OWNED_FILE"

printf '{"uid":%s,"gid":%s,"username":"%s","sudoAllowed":%s,"dockerSocketPresent":%s,"configReadable":%s,"configWritable":%s,"learnedReadable":%s,"learnedWritable":%s,"stateSecretVisible":%s,"foreignSocketVisible":%s,"sessionSocketVisible":%s,"socketRootEntries":%s,"networkReachable":%s}\n' \
    "$uid" "$gid" "$username" \
    "$sudo_allowed" "$docker_socket_present" "$config_readable" \
    "$config_writable" "$learned_readable" "$learned_writable" \
    "$state_secret_visible" "$foreign_socket_visible" \
    "$session_socket_visible" "$socket_root_entries" "$network_reachable" \
    > "$PEEN_PROBE_RESULT"
`

// readonlyProbeScript is fixed text like probeScript. It talks to the Docker
// API only through DOCKER_HOST, the way a Docker client in the worker would,
// and records each answer's status and body in the workspace.
const readonlyProbeScript = `
set -u

proxy_socket="${DOCKER_HOST#unix://}"

http_status() {
    local output="$1"
    shift
    curl --silent --max-time 20 --output "$output" --write-out '%{http_code}' \
        --unix-socket "$proxy_socket" "$@"
}

ping_status="$(http_status /dev/null http://docker/_ping)"
list_status="$(http_status "$PEEN_PROBE_LIST_BODY" \
    http://docker/v1.43/containers/json)"
inspect_status="$(http_status "$PEEN_PROBE_INSPECT_BODY" \
    "http://docker/v1.43/containers/$(hostname)/json")"
create_status="$(http_status "$PEEN_PROBE_CREATE_BODY" \
    --request POST --header 'Content-Type: application/json' \
    --data '{"Image":"peen-readonly-probe-never-created"}' \
    http://docker/v1.43/containers/create)"

docker_socket_present=false
if [ -e /var/run/docker.sock ]; then
    docker_socket_present=true
fi

printf '{"uid":%s,"dockerHost":"%s","pingStatus":"%s","listStatus":"%s","inspectStatus":"%s","createStatus":"%s","dockerSocketPresent":%s}\n' \
    "$(id -u)" "$DOCKER_HOST" \
    "$ping_status" "$list_status" "$inspect_status" "$create_status" \
    "$docker_socket_present" \
    > "$PEEN_PROBE_RESULT"
`

// readonlyProbeResult is what the readonly probe reports.
type readonlyProbeResult struct {
	UID                 int    `json:"uid"`
	DockerHost          string `json:"dockerHost"`
	PingStatus          string `json:"pingStatus"`
	ListStatus          string `json:"listStatus"`
	InspectStatus       string `json:"inspectStatus"`
	CreateStatus        string `json:"createStatus"`
	DockerSocketPresent bool   `json:"dockerSocketPresent"`
}

// probeResult is what the container reports about itself.
type probeResult struct {
	UID                  int    `json:"uid"`
	GID                  int    `json:"gid"`
	Username             string `json:"username"`
	SudoAllowed          bool   `json:"sudoAllowed"`
	DockerSocketPresent  bool   `json:"dockerSocketPresent"`
	ConfigReadable       bool   `json:"configReadable"`
	ConfigWritable       bool   `json:"configWritable"`
	LearnedReadable      bool   `json:"learnedReadable"`
	LearnedWritable      bool   `json:"learnedWritable"`
	StateSecretVisible   bool   `json:"stateSecretVisible"`
	ForeignSocketVisible bool   `json:"foreignSocketVisible"`
	SessionSocketVisible bool   `json:"sessionSocketVisible"`
	SocketRootEntries    int    `json:"socketRootEntries"`
	NetworkReachable     bool   `json:"networkReachable"`
}
