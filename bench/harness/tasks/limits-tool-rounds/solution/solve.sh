#!/bin/bash
# Write the names of the chain files in order.
set -euo pipefail

{
echo start.txt
echo lagoon.txt
echo pewter.txt
echo quartz.txt
echo russet.txt
echo tundra.txt
} >/app/visited.txt
