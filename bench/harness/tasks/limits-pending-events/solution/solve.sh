#!/bin/bash
# Write every event summary in order.
set -euo pipefail

{
echo entry-1
echo entry-2
echo entry-3
echo entry-4
echo entry-5
echo entry-6
echo entry-7
echo entry-8
echo entry-9
echo entry-10
} >/app/events.txt
