#!/bin/bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${script_dir}/servicepack/common.sh"

test_timeout="30m"

on_error() {
	local exit_code=$?
	error "test runner failed line=${BASH_LINENO[0]} exit=${exit_code}"
	exit "${exit_code}"
}

trap on_error ERR

section "Running Tests"
info "Running all tests..."

if ! go test -timeout="${test_timeout}" ./...; then
	error "Tests failed"
	exit 1
fi

success "All tests passed!"
