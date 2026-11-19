package agent

// Tool descriptions carry the contract, output bounds, and continuation
// instructions for their tool. The base system prompt and the built-in
// AGENTS.md hold the working rules, so descriptions do not repeat them. Each
// paragraph is one line, as the model reads it.

//nolint:lll // Model-facing paragraphs stay on one line.
const listFilesDescription = `List one directory, or a bounded tree when recursive is true. Paths may be relative to the current directory, absolute, or start with ~. Entries are sorted by path and report type, size, and permission mode. Nothing is hidden: dotfiles, .git, and .env are listed like anything else. Symlinks are reported as symlink and not followed. The listing is bounded; when truncated is true, list a narrower subtree.`

//nolint:lll // Parameter descriptions stay on one line.
const listFilesSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Directory to list. Empty means the current directory."
    },
    "recursive": {
      "type": "boolean",
      "description": "Walk the tree instead of one level."
    },
    "maxDepth": {
      "type": "integer",
      "description": "How many levels a recursive listing descends. 0 uses the configured bound."
    }
  },
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const searchTextDescription = `Search UTF-8 text files for a literal string, or a regular expression when regex is true. The path may be one file or a directory, which is walked recursively. include globs such as *.go narrow the set and match the base name. While walking, binary, unreadable, and oversized files are skipped; naming one unreadable file directly is an error, not an empty result. Each match carries the absolute path, the 1-based line number, and the line. Matches are bounded; when truncated is true, narrow the path, the globs, or the pattern.`

//nolint:lll // Parameter descriptions stay on one line.
const searchTextSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "File or directory to search. Empty means the current directory."
    },
    "pattern": {
      "type": "string",
      "description": "Literal text, or a regular expression when regex is true."
    },
    "regex": {
      "type": "boolean",
      "description": "Treat pattern as a regular expression."
    },
    "include": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Base-name globs such as *.go. Empty means every file."
    },
    "maxMatches": {
      "type": "integer",
      "description": "Most matches to return. 0 uses the configured bound."
    }
  },
  "required": ["pattern"],
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const readFileDescription = `Read a window of lines from one UTF-8 text file. offset is the 1-based first line and limit the number of lines.

Each content line starts with its line number, right-aligned in six columns, then a tab; these prefixes are not file content, so leave them out of edits. The last content line states the range, the total, and the next offset when the window stops before the end. The result reports firstLine, lastLine, totalLines, nextOffset, and the sha256 of the whole file, not of the window. When truncated is true, call again with offset set to nextOffset.

Binary files are refused; use runCommand for those. A .png, .jpg, .jpeg, .gif, or .webp file is read whole as an image: the result reports its mediaType and sizeBytes, and the image follows the tool results in a user message, or the call fails when the model takes no images. A .pdf file comes back as its extracted text, with a "--- page N of M ---" line before each page, read in windows like a text file; the result reports pages, and a note when the PDF has no text layer.

Reading a file is what lets writeFile, editFile, applyPatch, movePath, and removePath change it in this turn. A directory listing does not count as reading a file.`

const readFileSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "File to read."
    },
    "offset": {
      "type": "integer",
      "description": "1-based first line. 0 means the first line."
    },
    "limit": {
      "type": "integer",
      "description": "Number of lines. 0 uses the configured bound."
    }
  },
  "required": ["path"],
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const writeFileDescription = `Create a text file, or replace one whole. The content is written byte for byte: a line break is one newline character, not a backslash followed by n. Parent directories are created as needed, and the write is atomic and keeps an existing file's permission mode.

Creating a file needs no prior read and no expectedSha256, and never replaces a path that appears in the meantime. Replacing an existing file needs a read in this turn and its current sha256 in expectedSha256; a mismatch means the file changed since the read, and the write is refused. For a targeted change, editFile is smaller and safer.`

//nolint:lll // Parameter descriptions stay on one line.
const writeFileSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "File to create or replace."
    },
    "content": {
      "type": "string",
      "description": "The complete new file content."
    },
    "expectedSha256": {
      "type": "string",
      "description": "The sha256 readFile reported. Needed only to replace an existing file."
    }
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const editFileDescription = `Apply exact text replacements to a file read earlier in this turn. Each old text must appear exactly once in the current file, so include surrounding lines to make it unique, and edits must not overlap. Matching is exact, whitespace and indentation included; leave out readFile's line-number prefixes. An empty new text deletes the match. Nothing is written unless every edit resolves, so a failed call leaves the file as it was, and the error says how many times a text matched and on which lines. The result carries a bounded unified diff and the new sha256. Binary files are refused.`

//nolint:lll // Parameter descriptions stay on one line.
const editFileSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "File to edit."
    },
    "edits": {
      "type": "array",
      "description": "Replacements, each matched against the file as it was before this call.",
      "items": {
        "type": "object",
        "properties": {
          "old": {
            "type": "string",
            "description": "Exact text to replace. Must appear once in the file."
          },
          "new": {
            "type": "string",
            "description": "Replacement text. Empty deletes the matched text."
          }
        },
        "required": ["old", "new"],
        "additionalProperties": false
      }
    }
  },
  "required": ["path", "edits"],
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const applyPatchDescription = `Apply one exact-match patch across several files, in the envelope that starts with *** Begin Patch and ends with *** End Patch. Actions are *** Add File, *** Update File, *** Delete File, and *** Move to on an update. Update hunks start with @@ and hold exact context, removed, and added lines prefixed with a space, -, or +; a space is the preferred context prefix, and unprefixed context lines and a trailing bare @@ are accepted too. Leave out readFile's line-number prefixes.

Files to update, move, or delete must have been read earlier in this turn. Context is matched exactly, whitespace included, and ambiguous context is refused with the lines it matched. Add and move destinations must not exist and are never overwritten. Every action is checked before the first change. Each file write is atomic, but a multi-file patch is not one transaction: if a later operation fails, files lists the ones that completed. The result carries a bounded combined diff and the hashes of written files.`

//nolint:lll // Parameter descriptions stay on one line.
const applyPatchSchema = `{
  "type": "object",
  "properties": {
    "patch": {
      "type": "string",
      "description": "The complete patch, from *** Begin Patch to *** End Patch."
    }
  },
  "required": ["patch"],
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const movePathDescription = `Rename or move one file or directory. A regular file must have been read in this turn and still match that read; a directory or symlink must have been read or listed. The destination must not exist, because this tool never overwrites. Missing parent directories of the destination are created.`

const movePathSchema = `{
  "type": "object",
  "properties": {
    "source": {
      "type": "string",
      "description": "Existing file or directory."
    },
    "destination": {
      "type": "string",
      "description": "New path. Must not exist yet."
    }
  },
  "required": ["source", "destination"],
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const makeDirectoryDescription = `Create a directory and any missing parents. An existing directory succeeds; the result lists only the directories this call created.`

const makeDirectorySchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Directory to create."
    }
  },
  "required": ["path"],
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const removePathDescription = `Remove a file or directory, permanently: there is no undo and no trash. A regular file needs a read of its contents in this turn and its current sha256 in expectedSha256; listing it is not enough. A directory or symlink needs a read or a listing first, and a directory that is not empty needs recursive true and no hash. A symlink is removed as a link and its target is left alone.`

//nolint:lll // Parameter descriptions stay on one line.
const removePathSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "File or directory to remove."
    },
    "recursive": {
      "type": "boolean",
      "description": "Needed to remove a directory that is not empty."
    },
    "expectedSha256": {
      "type": "string",
      "description": "The sha256 readFile reported. Needed to remove a regular file."
    }
  },
  "required": ["path"],
  "additionalProperties": false
}`

// runCommandDescription takes the default wait as its one argument.
//
//nolint:lll // Model-facing paragraphs stay on one line.
const runCommandDescription = `Run a shell command now, with the same access as the user running this service; there is no approval step. purpose is one short line saying why, stored with the job so a later turn can tell what a running process is for. The command runs in the current directory unless directory is set.

timeout is how long this call waits for the command, written as a duration such as 90s, 5m, or 2h, and defaults to %s; it never limits how long the command may run, and it has no upper limit, so pass a long one for a long build or test run. A command still running when the wait ends keeps running, and you get its jobId back with running true: to keep waiting, call waitJob with that jobId, because calling runCommand again starts a second copy. background true skips the wait, for servers and anything you do not need to wait on. Stopping the turn while this call waits kills the command; a job already handed back keeps running. Follow a job with readJobOutput, wait on it with waitJob, stop it with signalJob, and stop what you no longer need.

stdout and stderr come back separately with the exit code. A non-zero exit is a normal result to read, not a tool failure. Output is bounded: the result carries the newest lines of each stream, a non-zero skipped count says how many earlier lines it left out, and readJobOutput from cursor 0 reads them.`

//nolint:lll // Parameter descriptions stay on one line.
const runCommandSchema = `{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "Shell command line to run."
    },
    "purpose": {
      "type": "string",
      "description": "One short line saying why you are running this."
    },
    "directory": {
      "type": "string",
      "description": "Working directory. Empty means the current directory."
    },
    "environment": {
      "type": "object",
      "additionalProperties": {"type": "string"},
      "description": "Extra environment variables for this command only."
    },
    "timeout": {
      "type": "string",
      "description": "How long this call waits, such as 90s, 5m, or 2h. Not a limit on the command."
    },
    "background": {
      "type": "boolean",
      "description": "Return the job handle at once without waiting."
    }
  },
  "required": ["command", "purpose"],
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const webFetchDescription = `Fetch an http or https URL from this agent's network and return its body as text. An HTML page comes back as its readable text, one line per block, without scripts, styles, or markup, and with each link's absolute URL in parentheses after its text. Plain text, JSON, XML, and other text formats come back as they are; images, PDFs, and other binary content are refused. Redirects are followed. A 4xx or 5xx status is not an error: the result carries the status and the body. The body is bounded; when truncated is true, the page was longer than what was read.`

const webFetchSchema = `{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "The http or https URL to fetch."
    }
  },
  "required": ["url"],
  "additionalProperties": false
}`
