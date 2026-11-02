#!/bin/sh
# Deploy the service. It refuses to start until .env sets every variable the
# service reads.
set -eu
cd "$(dirname "$0")"

set -a
. ./.env
set +a

for name in DATABASE_URL API_TOKEN SENTRY_DSN REGION LOG_LEVEL; do
	eval "value=\${${name}:-}"
	if [ -z "${value}" ]; then
		echo "deploy: configuration incomplete" >&2
		exit 1
	fi
done

echo "deploy: starting"
