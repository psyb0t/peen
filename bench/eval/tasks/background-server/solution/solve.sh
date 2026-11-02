#!/bin/bash
# Start the server in the background, wait for it, fetch the answer, stop it.
set -euo pipefail

/app/run-server.sh &
server=$!
trap 'kill "${server}"' EXIT

for _ in $(seq 1 30); do
	if curl -fsS http://127.0.0.1:8099/answer -o /app/ANSWER.txt; then
		exit 0
	fi
	sleep 1
done

exit 1
