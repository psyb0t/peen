#!/bin/bash
set -euo pipefail

log() {
	local level="$1"
	shift
	local timestamp file line function_name message
	timestamp="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
	file="${BASH_SOURCE[1]##*/}"
	line="${BASH_LINENO[0]}"
	function_name="${FUNCNAME[1]:-main}"
	message="$*"
	printf '{"time":"%s","level":"%s","file":"%s","line":%d,"func":"%s","msg":"%s"}\n' \
		"$timestamp" "$level" "$file" "$line" "$function_name" "$message" >&2
}

operation="${1:-}"
case "$operation" in
build | check | format | generate | lint | pkg-add | pkg-lock | pkg-remove | pkg-update | pkg-upgrade | test) ;;
*)
	log ERROR "operation must be build, check, format, generate, lint, pkg-add, pkg-lock, pkg-remove, pkg-update, pkg-upgrade, or test"
	exit 1
	;;
esac

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source_root="$repository_root/web"
temporary_root="$(mktemp -d)"
log_file="${LOG_FILE:-/tmp/peen-web-${operation}.log}"

cleanup() {
	if [[ -d "$temporary_root" ]]; then
		rm -rf "$temporary_root"
	fi
}

trap cleanup EXIT
trap 'log ERROR "command failed exit=$?"' ERR
exec > >(tee -a "$log_file") 2>&1

cp -a "$source_root/." "$temporary_root/"
cd "$temporary_root"

sync_generated_types() {
	corepack pnpm exec prettier --write "$temporary_root/src/lib/api/generated.ts"
	cp "$temporary_root/src/lib/api/generated.ts" \
		"$source_root/src/lib/api/generated.ts"
}

sync_package_files() {
	cp "$temporary_root/package.json" "$source_root/package.json"
	cp "$temporary_root/pnpm-lock.yaml" "$source_root/pnpm-lock.yaml"
}

sync_formatted_files() {
	cp -a "$temporary_root/src/." "$source_root/src/"
	for file in .prettierrc eslint.config.js package.json svelte.config.js tsconfig.json vite.config.ts; do
		cp "$temporary_root/$file" "$source_root/$file"
	done
}

normalize_built_assets() {
	local output_path="$repository_root/internal/pkg/http/server/web/dist"
	find "$output_path" -type f \
		\( -name '*.css' -o -name '*.html' -o -name '*.js' -o -name '*.json' \) \
		-exec sed -i 's/[[:space:]][[:space:]]*$//' {} +
}

case "$operation" in
pkg-lock)
	log INFO "refreshing frontend lockfile"
	corepack pnpm install --lockfile-only --ignore-scripts
	sync_package_files
	;;
pkg-add | pkg-update | pkg-remove)
	if [[ -z "${WEB_PKG:-}" ]]; then
		log ERROR "WEB_PKG is required"
		exit 1
	fi
	log INFO "changing one frontend package"
	case "$operation" in
	pkg-add)
		corepack pnpm add --lockfile-only --save-exact --ignore-scripts "$WEB_PKG"
		;;
	pkg-update)
		corepack pnpm update --lockfile-only --save-exact --ignore-scripts "$WEB_PKG"
		;;
	pkg-remove)
		corepack pnpm remove --lockfile-only --ignore-scripts "$WEB_PKG"
		;;
	esac
	sync_package_files
	;;
pkg-upgrade)
	log INFO "upgrading frontend packages under pnpm age policy"
	corepack pnpm update --lockfile-only --latest --save-exact --ignore-scripts
	sync_package_files
	;;
*)
	log INFO "installing frontend dependencies in container scratch"
	corepack pnpm install --frozen-lockfile --ignore-scripts
	corepack pnpm rebuild esbuild
	case "$operation" in
	generate)
		log INFO "generating TypeScript API types"
		PEEN_API_SPEC="$repository_root/api/api.yml" corepack pnpm run generate:api
		sync_generated_types
		;;
	build)
		log INFO "building embedded static control surface"
		PEEN_API_SPEC="$repository_root/api/api.yml" \
			PEEN_WEB_OUTPUT="$repository_root/internal/pkg/http/server/web/dist" \
			corepack pnpm run build
		normalize_built_assets
		sync_generated_types
		;;
	check | lint | test)
		log INFO "running frontend ${operation}"
		corepack pnpm run "$operation"
		;;
	format)
		log INFO "formatting frontend source"
		corepack pnpm run format
		sync_formatted_files
		;;
	esac
	;;
esac
