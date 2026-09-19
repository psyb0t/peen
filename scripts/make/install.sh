#!/bin/bash

set -euo pipefail

# Source common functions. The path is relative to the search path lint.sh
# hands shellcheck, which is the servicepack script directory itself.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "$SCRIPT_DIR/servicepack/common.sh"

APP_NAME="$(head -n 1 go.mod | awk '{print $2}' | awk -F'/' '{print $NF}')"
readonly APP_NAME

# PREFIX is where the binary lands. ~/bin needs no root and is already on PATH
# in most shells, which is why it is the default rather than /usr/local/bin.
readonly PREFIX="${PREFIX:-$HOME/bin}"
readonly BUILT="./build/${APP_NAME}"
readonly BINARY_MODE=0755

section "Installing $APP_NAME"

if [[ ! -f "$BUILT" ]]; then
	error "No built binary at $BUILT"
	info "Run 'make build' first, or 'make install' which builds for you."
	exit 1
fi

if [[ ! -d "$PREFIX" ]]; then
	info "Creating $PREFIX"
	mkdir -p "$PREFIX"
fi

# install(1) rather than cp: it sets the mode in one step and replaces the
# directory entry instead of writing through it, so a copy that is already
# running is not corrupted underneath itself.
install -m "$BINARY_MODE" "$BUILT" "$PREFIX/$APP_NAME"

success "Installed $PREFIX/$APP_NAME"

case ":${PATH}:" in
*":${PREFIX}:"*) ;;
*)
	info "$PREFIX is not on PATH. Add this to your shell profile:"
	info "    export PATH=\"\$PATH:$PREFIX\""
	;;
esac
