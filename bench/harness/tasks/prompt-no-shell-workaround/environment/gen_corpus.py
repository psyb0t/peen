"""Write the 2,000-file text corpus the task searches. Deterministic."""

import os
import random
import sys

TOKEN = "ember-quartz-7731"
DECOY = "ember-quartz-7737"
WORDS = (
    "alpha bravo cedar delta ember fjord garnet harbor indigo juniper kestrel "
    "lantern meadow nectar orchid pebble quartz ripple sable thistle umber "
    "velvet willow xenon yarrow zephyr"
).split()

root = sys.argv[1]
rng = random.Random(7331)
index = 0
for group in range(40):
    directory = os.path.join(root, f"group{group:02d}", f"set{group % 7}")
    os.makedirs(directory, exist_ok=True)
    for number in range(50):
        lines = [" ".join(rng.choice(WORDS) for _ in range(8)) for _ in range(6)]
        if index % 83 == 17:
            lines[rng.randrange(6)] += " " + TOKEN
        elif index % 97 == 5:
            lines[rng.randrange(6)] += " " + DECOY
        suffix = ".log" if number % 3 == 0 else ".txt"
        with open(os.path.join(directory, f"file{number:02d}{suffix}"), "w") as handle:
            handle.write("\n".join(lines) + "\n")
        index += 1
