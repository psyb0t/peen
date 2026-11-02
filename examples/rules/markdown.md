---
paths:
  - "**/*.md"
  - "**/*.mdx"
---

# Markdown

Sources: the [CommonMark specification](https://spec.commonmark.org/) and the default rules of [markdownlint](https://github.com/DavidAnson/markdownlint).

Follow the project's own docs style and markdownlint configuration first. Use this rule where the project says nothing.

- Give a document one top-level `#` heading and do not skip heading levels.
- Put a blank line before and after headings, lists, tables, and code blocks.
- Fence code blocks and name their language: ` ```bash `.
- Link to files in the repository with relative paths, and check that every link and anchor you add resolves.
- Keep the file's existing line wrapping. Do not reflow paragraphs you did not change.
- When code changes behavior, a command, or a setting, update the docs that describe it in the same change, and check that every command you document runs as written.
