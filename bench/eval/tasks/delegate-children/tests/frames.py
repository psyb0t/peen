"""Print how many launchAgent calls Peen recorded in its frame stream."""

import json
import sys

FRAMES_PATH = "/peen/frames.jsonl"
TOOL_USE = "tool.use"
LAUNCH_AGENT = "launchAgent"


def launch_agent_calls(path):
    count = 0
    with open(path) as frames:
        for line in frames:
            frame = json.loads(line)
            if frame.get("type") != TOOL_USE:
                continue
            if frame.get("data", {}).get("name") == LAUNCH_AGENT:
                count += 1
    return count


if __name__ == "__main__":
    print(launch_agent_calls(sys.argv[1] if len(sys.argv) > 1 else FRAMES_PATH))
