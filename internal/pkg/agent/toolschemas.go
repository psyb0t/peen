package agent

// Tool descriptions carry the usage rules, output bounds, and continuation
// instructions for their tool. The base system prompt does not repeat them.

const listFilesDescription = `List one directory, or a bounded tree.
Set recursive to walk the tree instead of one level.
Paths may be relative to the current workspace, absolute, or start with ~.
Entries are sorted by path and report type, size, and permission mode.
Nothing is hidden: dotfiles, .git, and .env are listed like anything else.
Symlinks are reported as symlink and are not followed.
The listing is bounded. When truncated is true, list a narrower subtree.`

const listFilesSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Directory to list. Empty means the current workspace."
    },
    "recursive": {
      "type": "boolean",
      "description": "Walk the tree instead of one level."
    },
    "maxDepth": {
      "type": "integer",
      "description": "Recursion depth. 0 uses the configured bound."
    }
  },
  "additionalProperties": false
}`

const searchTextDescription = `Search UTF-8 text files for text.
The pattern is a literal string, or a regular expression when regex is
true.
Path may be a single file or a directory, which is walked recursively.
Use include globs such as *.go to narrow the set; they match the base name.
While walking, binary, unreadable, and oversized files are skipped silently.
Naming one unreadable file directly is an error, not an empty result.
Results carry the absolute path, the 1-based line number, and the line.
Matches are bounded. When truncated is true, narrow the path, the globs,
or the pattern.`

const searchTextSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "File or directory to search. Empty means the workspace."
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
      "description": "Base-name globs. Empty means every file."
    },
    "maxMatches": {
      "type": "integer",
      "description": "Match bound. 0 uses the configured bound."
    }
  },
  "required": ["pattern"],
  "additionalProperties": false
}`

const readFileDescription = `Read a line window from one UTF-8 text file.
offset is the 1-based first line and limit is the line count.
The result reports firstLine, lastLine, totalLines, and the sha256 of the
WHOLE file, not of the window.
When truncated is true, call again with offset set to the nextOffset.
Binary files are rejected. Use run_command for those.
Reading a file is what makes it eligible for write_file, edit_file,
apply_patch, move_path, and remove_path in this turn. A directory listing
does not count as reading a regular file.`

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
      "description": "Line count. 0 uses the configured bound."
    }
  },
  "required": ["path"],
  "additionalProperties": false
}`

const writeFileDescription = `Create a text file, or replace one.
This writes the file's entire content.
Parent directories are created as needed.
Creating a NEW file needs no expectedSha256 and no prior read.
The target is checked for absence and creation never replaces a path that
appears concurrently.
Replacing an EXISTING file requires reading it first in this turn and
passing its current expectedSha256. A mismatch means the file changed
underneath you and the write is refused.
Prefer edit_file for a targeted change. This tool replaces everything.
The write is atomic and preserves the existing file's permission mode.`

const writeFileSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "File to create or replace."
    },
    "content": {
      "type": "string",
      "description": "Complete new file content."
    },
    "expectedSha256": {
      "type": "string",
      "description": "Current sha256. Required only when replacing a file."
    }
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`

const editFileDescription = `Apply exact text replacements to a file.
The file must have been read earlier in this turn.
Every old string must appear EXACTLY ONCE in the current file. Include
surrounding lines to make it unique. Edits must not overlap.
Matching is exact, including whitespace and indentation. An empty new
string deletes the matched text.
Nothing is written unless every edit resolves, so a failed call leaves
the file untouched.
The result carries a bounded unified diff and the new sha256.
Binary files are rejected.`

const editFileSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "File to edit."
    },
    "edits": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "old": {
            "type": "string",
            "description": "Exact text to replace. Must be unique in the file."
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

const applyPatchDescription = `Apply one exact-match multi-file patch.
Use the Codex patch envelope with *** Begin Patch and *** End Patch.
Supported actions are *** Add File, *** Update File, *** Delete File, and
*** Move to on an update. Update hunks use @@ plus exact context, removal,
and addition lines prefixed with a space, -, or +. A leading space is the
preferred context prefix. Unprefixed context lines and a trailing bare @@
delimiter are also accepted for compatible coding-model patch emitters.
Existing source files must have been read earlier in this turn. Context is
matched exactly, including whitespace, and ambiguous matches are rejected.
Add and move destinations must not exist, and are never overwritten.
All actions are checked before the first mutation. Individual writes are
atomic, but a multi-file patch is not globally transactional. If an
unexpected later operation fails, files lists the operations that completed.
The result includes a bounded combined diff and hashes for written files.`

const applyPatchSchema = `{
  "type": "object",
  "properties": {
    "patch": {
      "type": "string",
      "description": "Complete patch document, including begin and end markers."
    }
  },
  "required": ["patch"],
  "additionalProperties": false
}`

const movePathDescription = `Rename one file or directory.
Also use this to move something between directories.
Regular files must have been read in this turn and still match that read.
Directories and symlinks must have been read or listed in this turn.
The destination must NOT exist. This tool never overwrites.
Missing parent directories of the destination are created.`

const movePathSchema = `{
  "type": "object",
  "properties": {
    "source": {
      "type": "string",
      "description": "Existing file or directory."
    },
    "destination": {
      "type": "string",
      "description": "New path. Must not already exist."
    }
  },
  "required": ["source", "destination"],
  "additionalProperties": false
}`

const makeDirectoryDescription = `Create a directory and any missing parents.
An existing directory succeeds and reports nothing created.
The result lists only the directories this call actually created.`

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

const removePathDescription = `Remove a file or directory.
Removing a FILE requires reading its contents in this turn and passing its
current expectedSha256. Listing a regular file is not enough.
Removing a DIRECTORY or symlink requires reading or listing it first.
Removing a non-empty DIRECTORY requires recursive true, and no hash.
A symlink is removed as a link. Its target is left alone.
Removal is permanent. There is no undo and no trash.`

const removePathSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "File or directory to remove."
    },
    "recursive": {
      "type": "boolean",
      "description": "Required to remove a non-empty directory tree."
    },
    "expectedSha256": {
      "type": "string",
      "description": "Current sha256. Required when removing a regular file."
    }
  },
  "required": ["path"],
  "additionalProperties": false
}`

const runCommandDescription = `Run a shell command immediately.
There is no approval step and no confirmation. The command runs with
the same access as the user running this service.
State the purpose: one short line saying why you are running it. It is
stored with the job so a later turn can tell what a running process is for.
It runs in the current workspace unless directory is set.
timeoutSeconds bounds how long THIS CALL WAITS, never how long the command
may run. A command still running when the bound elapses is left running and
you get a jobId back, with running true. That is how you start builds,
servers, and long test runs. Use background true to skip the wait entirely.
Follow a running job with read_job_output, block on it with wait_job, and
stop it with signal_job. Clean up what you no longer need.
stdout and stderr come back separately with the exit status. A non-zero exit
is a normal result to read and react to, not a tool failure.
Output is bounded and drops oldest, reporting how many lines it dropped.
Prefer the file tools for reading and editing. They are bounded and
report content hashes. Do not use shell redirection, cp, mv, sed -i, or
similar shell operations for manual file changes. Use the file tools so
existing files are read first and new destinations cannot be overwritten.`

const runCommandSchema = `{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "Shell command line to execute."
    },
    "purpose": {
      "type": "string",
      "description": "One short line saying why you are running this."
    },
    "directory": {
      "type": "string",
      "description": "Working directory. Empty means the current workspace."
    },
    "environment": {
      "type": "object",
      "additionalProperties": {"type": "string"},
      "description": "Extra environment variables for this command only."
    },
    "timeoutSeconds": {
      "type": "integer",
      "description": "How long to WAIT, not a kill bound. 0 uses the default."
    },
    "background": {
      "type": "boolean",
      "description": "Skip the wait and return the job handle at once."
    }
  },
  "required": ["command", "purpose"],
  "additionalProperties": false
}`
