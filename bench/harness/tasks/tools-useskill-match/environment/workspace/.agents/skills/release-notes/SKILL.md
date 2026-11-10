---
name: release-notes
description: Write RELEASE_NOTES.md from a list of changes. Use it whenever release notes are asked for.
---
# Release notes

Write `RELEASE_NOTES.md` in the workspace root in this exact shape:

1. The first line is `# Release notes`, followed by one blank line.
2. Then one line per change, in the original order, as `* <message> (<type>)`. The `<type>` is the word before the colon in the change, and the `<message>` is the text after the colon and the space.
