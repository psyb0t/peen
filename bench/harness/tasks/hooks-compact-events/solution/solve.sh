#!/bin/bash
set -euo pipefail

for part in 1 2 3 4; do
	tail -n 1 "/app/data/part${part}.txt" >"/app/out/${part}.txt"
done
