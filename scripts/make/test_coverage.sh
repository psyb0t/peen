#!/bin/bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${script_dir}/servicepack/common.sh"

on_error() {
	local exit_code=$?
	error "coverage runner failed line=${BASH_LINENO[0]} exit=${exit_code}"
	exit "${exit_code}"
}

trap on_error ERR

minimum_coverage=${MIN_TEST_COVERAGE:-90}
test_timeout="30m"

section "Running Tests with Coverage Check"

module="$(go list -m)"
info "module=${module} coverage floor=${minimum_coverage}%"

coverage_data_directory="${SERVICEPACK_COVDATA_DIR:-$PWD/.cover/covdata}"
export SERVICEPACK_COVDATA_DIR="${coverage_data_directory}"
rm -rf "${coverage_data_directory}"
mkdir -p "${coverage_data_directory}"

profile_raw="coverage.txt"
profile_covdata="coverage_covdata.txt"
profile_merged="coverage_merged.txt"
profile_filtered="coverage_filtered.txt"

cleanup() {
	rm -f "${profile_raw}" "${profile_covdata}" "${profile_merged}" \
		"${profile_filtered}"
}

trap cleanup EXIT

merge_profiles() {
	local output="$1"
	shift

	{
		printf 'mode: atomic\n'
		awk '
			FNR == 1 && $1 == "mode:" { next }
			{
				key = $1 SUBSEP $2
				if (!(key in count) || $3 > count[key]) {
					count[key] = $3
				}
				block[key] = $1 " " $2
			}
			END {
				for (key in block) {
					print block[key], count[key]
				}
			}
		' "$@" | sort -k1,1 -k2,2
	} >"${output}"
}

info "running unit and integration tests with coverage..."
if ! go test -count=1 -timeout="${test_timeout}" -tags=integration \
	-coverpkg="${module}/..." -coverprofile="${profile_raw}" ./...; then
	error "Tests failed"
	exit 1
fi

if find "${coverage_data_directory}" -type f -name 'covcounters.*' -print -quit | grep -q .; then
	info "merging out-of-process service covdata..."
	go tool covdata textfmt -i="${coverage_data_directory}" -o="${profile_covdata}"
	merge_profiles "${profile_merged}" "${profile_raw}" "${profile_covdata}"
else
	cp "${profile_raw}" "${profile_merged}"
fi

gate_exclude='(/cmd/|/tests/|\.gen\.go:|/internal/pkg/service-manager/mocks\.go:|/internal/pkg/services/(example-|hello-world/))'
awk -v exclude="${gate_exclude}" '$1 !~ exclude' \
	"${profile_merged}" >"${profile_filtered}"

coverage_summary="$(go tool cover -func="${profile_filtered}" | awk '$1 == "total:" { print $3 }')"
percentage=${coverage_summary%%%}
integer_percentage=${percentage%%.*}

printf '%s\n' "${percentage}" >coverage-percent.txt

if [[ -z "${percentage}" ]]; then
	warning "No test coverage information available"
	exit 1
fi

if [[ "${integer_percentage}" -lt "${minimum_coverage}" ]]; then
	error "Coverage ${percentage}% is less than the minimum ${minimum_coverage}%"
	exit 1
fi

success "Coverage ${percentage}% meets the minimum requirement of ${minimum_coverage}%"
