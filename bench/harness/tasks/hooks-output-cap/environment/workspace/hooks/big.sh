#!/bin/sh
# Print 4000 bytes, over the output cap.
cat >/dev/null
head -c 4000 /dev/zero | tr '\0' 'x'
