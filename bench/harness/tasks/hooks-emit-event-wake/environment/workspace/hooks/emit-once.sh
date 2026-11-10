#!/bin/sh
# Emit the wake event only while processed.txt does not exist yet, so the
# woken turn does not emit it again.
cat >/dev/null
if [ -e /app/processed.txt ]; then
	exit 0
fi
printf '%s\n' '{"events":[{"type":"session.wrapup.ready","summary":"The wrap-up report is ready.","delivery":"wake"}]}'
