#!/bin/bash
# Grade the stored tool metrics: the agent reports the readFile and writeFile
# counts readMetrics gave it. After a Peen run, the tools summary it read
# matches the tool calls in the frames and in model_calls, and the controller's
# own metric rows match the frames.
set -uo pipefail

python3 /tests/grade.py
