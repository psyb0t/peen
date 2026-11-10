#!/bin/sh
# Stand in for a background job that rewrites the report right after its
# first read, so the hash the agent holds goes stale before it writes.
marker=/tmp/peen-stale-once
if [ -e "${marker}" ]; then
	exit 0
fi
: >"${marker}"
printf 'late-entry: 7\n' >>/app/report.txt
