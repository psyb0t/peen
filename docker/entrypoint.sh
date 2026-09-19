#!/bin/bash
#
# Peen container entrypoint.
#
# The image has two roles and one entrypoint. Control mode starts as the fixed
# non-root image account. A Docker worker starts as root only long enough to
# create or reconcile the controller's host account, then drops to it.
#
# That bootstrap is needed even without privilege escalation. Running a bare
# numeric UID and GID preserves file ownership but leaves the host username
# absent from the image. When the profile permits escalation, the same bootstrap
# also grants that account passwordless sudo. The agent never runs as root.
#
# Every input is set by the controller from operator configuration. None of it
# comes from a client request or from model output.
#
# This script does not tee to a log file. It execs the worker, which reads its
# launch document from stdin, and a tee through process substitution would
# replace the stream the worker needs. Container logs are the record here.

set -euo pipefail

readonly ROOT_ID=0
readonly SUDOERS_FILE="/etc/sudoers.d/peen-worker"
readonly SUDOERS_MODE=0440
readonly UNKNOWN_GROUP_PREFIX="peen-gid-"

log() {
	local level="$1"
	shift

	local timestamp
	timestamp="$(date -u '+%Y-%m-%dT%H:%M:%S.%3NZ')"

	printf '{"time":"%s","level":"%s","file":"%s","line":%d,"func":"%s","msg":"%s"}\n' \
		"$timestamp" \
		"$level" \
		"${BASH_SOURCE[1]##*/}" \
		"${BASH_LINENO[0]}" \
		"${FUNCNAME[1]:-main}" \
		"$*" >&2
}

fail() {
	log ERROR "$*"

	exit 1
}

require_numeric() {
	local name="$1" value="$2"

	[[ "$value" =~ ^[0-9]+$ ]] || fail "$name must be numeric"
}

# ensure_account creates or reconciles the user the worker drops to. The image
# cannot ship this account because its IDs and name come from the controller's
# own host identity.
ensure_account() {
	local username="$1" uid="$2" gid="$3" home="$4" existing_user

	if ! getent group "$gid" >/dev/null; then
		if getent group "$username" >/dev/null; then
			fail "worker group name already belongs to another GID"
		fi

		groupadd --gid "$gid" "$username"
	fi

	if existing_user="$(getent passwd "$uid" | cut -d: -f1)"; then
		:
	else
		existing_user=""
	fi

	if [[ -z "$existing_user" ]]; then
		if getent passwd "$username" >/dev/null; then
			fail "worker username already belongs to another UID"
		fi

		useradd \
			--uid "$uid" \
			--gid "$gid" \
			--home-dir "$home" \
			--shell /bin/bash \
			--no-create-home \
			"$username"
	elif [[ "$existing_user" != "$username" ]]; then
		if getent passwd "$username" >/dev/null; then
			fail "worker username already belongs to another UID"
		fi

		usermod \
			--login "$username" \
			--home "$home" \
			--gid "$gid" \
			--shell /bin/bash \
			"$existing_user"
	else
		usermod \
			--home "$home" \
			--gid "$gid" \
			--shell /bin/bash \
			"$username"
	fi
}

add_supplementary_groups() {
	local username="$1" gids="$2"

	[[ -n "$gids" ]] || return 0

	local gid_list=() gid group_name
	IFS=',' read -ra gid_list <<<"$gids"

	for gid in "${gid_list[@]}"; do
		require_numeric PEEN_WORKER_SUPPLEMENTARY_GIDS "$gid"

		group_name="$(getent group "$gid" | cut -d: -f1)"
		if [[ -z "$group_name" ]]; then
			group_name="${UNKNOWN_GROUP_PREFIX}${gid}"
			groupadd --gid "$gid" "$group_name"
		fi

		usermod --append --groups "$group_name" "$username"
	done

	log INFO "added the worker account to its supplementary groups"
}

# grant_sudo is reached only for a profile the operator marked
# allowPrivilegeEscalation. Every other profile leaves the image's sudoers
# untouched, so sudo has nothing to authorize.
grant_sudo() {
	local username="$1"

	command -v sudo >/dev/null ||
		fail "the profile allows privilege escalation but sudo is missing"

	printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$username" >"$SUDOERS_FILE"
	chmod "$SUDOERS_MODE" "$SUDOERS_FILE"
	visudo --check --file="$SUDOERS_FILE" >/dev/null

	log INFO "granted passwordless sudo to the worker account"
}

main() {
	# A container that is already non-root is the control plane. Every Docker
	# worker explicitly starts as root and reaches the bootstrap below.
	if [[ "$(id -u)" -ne "$ROOT_ID" ]]; then
		exec "$@"
	fi

	local worker_uid="${PEEN_WORKER_UID:-}"
	local worker_gid="${PEEN_WORKER_GID:-}"
	local worker_username="${PEEN_WORKER_USERNAME:-}"
	local worker_home="${PEEN_WORKER_HOME:-}"

	[[ -n "$worker_uid" && -n "$worker_gid" && -n "$worker_username" ]] ||
		fail "started as root without a worker identity to drop to"

	require_numeric PEEN_WORKER_UID "$worker_uid"
	require_numeric PEEN_WORKER_GID "$worker_gid"

	[[ "$worker_uid" -ne "$ROOT_ID" && "$worker_gid" -ne "$ROOT_ID" ]] ||
		fail "refusing to run a worker as root"

	[[ "$worker_home" == /* ]] || fail "PEEN_WORKER_HOME must be absolute"

	ensure_account \
		"$worker_username" "$worker_uid" "$worker_gid" "$worker_home"
	add_supplementary_groups \
		"$worker_username" "${PEEN_WORKER_SUPPLEMENTARY_GIDS:-}"

	if [[ "${PEEN_WORKER_ALLOW_SUDO:-}" == "true" ]]; then
		grant_sudo "$worker_username"
	fi

	log INFO "dropping to the controller host account"

	# setpriv comes from util-linux in the base image, so dropping privileges
	# needs no extra dependency. --init-groups reads the account's groups, which
	# is why the account is created above rather than assumed.
	exec setpriv \
		--reuid "$worker_uid" \
		--regid "$worker_gid" \
		--init-groups \
		-- "$@"
}

main "$@"
