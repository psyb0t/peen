#!/bin/bash
# Make the four changes.
set -euo pipefail

cd /app/site
sed -i 's/draft/final/' index.txt
rm old.txt
mkdir -p archive
sed 's/todo/done/' notes.txt >archive/notes.txt
rm notes.txt
echo ok >done.txt
