package agent

// Job tool descriptions and schemas. Like the file tools, each description
// carries its own contract and bounds so the base system prompt does not have
// to. Each paragraph is one line, as the model reads it.

//nolint:lll // Model-facing paragraphs stay on one line.
const listJobsDescription = `List the commands this session has started, running and finished, with the purpose given when each started, its state, its timings, and how much output is buffered. state narrows the list; empty lists every job. Use it when you have lost track of what you started, or after being told a job finished.`

const listJobsSchema = `{
  "type": "object",
  "properties": {
    "state": {
      "type": "string",
      "enum": ["running", "exited", "signalled", "failed"],
      "description": "List only jobs in this state. Empty lists every job."
    }
  },
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const readJobOutputDescription = `Read a bounded window of one job's output without waiting for it to finish. Pass the cursors from a previous call to continue where you left off; 0 starts from the oldest line still buffered. A job's buffer is bounded, so a chatty process drops its oldest lines and reports how many in the dropped counts; a non-zero dropped count means you missed output, so read more often or have the command print less. found is false when the job is not one of this session's, which is a result, not an error.`

//nolint:lll // Parameter descriptions stay on one line.
const readJobOutputSchema = `{
  "type": "object",
  "properties": {
    "jobId": {
      "type": "string",
      "format": "uuid",
      "description": "The job to read, from runCommand or listJobs."
    },
    "stream": {
      "type": "string",
      "enum": ["stdout", "stderr", "both"],
      "description": "Which stream to read. Empty means both."
    },
    "stdoutCursor": {
      "type": "integer",
      "description": "stdoutCursor from a previous result. 0 reads from the oldest buffered line."
    },
    "stderrCursor": {
      "type": "integer",
      "description": "stderrCursor from a previous result. 0 reads from the oldest buffered line."
    },
    "maxLines": {
      "type": "integer",
      "description": "Most lines to return per stream. 0 uses the configured bound."
    }
  },
  "required": ["jobId"],
  "additionalProperties": false
}`

// waitJobDescription takes the default wait as its one argument.
//
//nolint:lll // Model-facing paragraphs stay on one line.
const waitJobDescription = `Wait until one job exits or the wait ends. The result carries the job's state and exit code and the output that arrived after the cursors you pass, as the newest lines of each stream; a non-zero skipped count says how many earlier lines after your cursor it left out, and readJobOutput from that cursor reads them. The last line the job printed is the last line of stdout or stderr here.

timeout is how long you wait, written as a duration such as 30s, 5m, or 2h, and defaults to %s; it never limits how long the job may run, and it has no upper limit, so pass a long one for long work. When the wait ends first, running is true and the job is still going: call waitJob again, or carry on with something else. found is false when the job is not one of this session's.`

//nolint:lll // Parameter descriptions stay on one line.
const waitJobSchema = `{
  "type": "object",
  "properties": {
    "jobId": {
      "type": "string",
      "format": "uuid",
      "description": "The job to wait for, from runCommand or listJobs."
    },
    "timeout": {
      "type": "string",
      "description": "How long to wait, such as 30s, 5m, or 2h. Not a limit on the job."
    },
    "stdoutCursor": {
      "type": "integer",
      "description": "stdoutCursor from a previous result. 0 reads from the oldest buffered line."
    },
    "stderrCursor": {
      "type": "integer",
      "description": "stderrCursor from a previous result. 0 reads from the oldest buffered line."
    }
  },
  "required": ["jobId"],
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const signalJobDescription = `Stop a running job. stop sends SIGTERM to the whole process group and then SIGKILL if it does not exit in time; kill sends SIGKILL at once. Both reach commands that forked or backgrounded children, so nothing is left behind. Signalling an unknown, exited, or already signalled job is not an error: the result reports its current state. A background job keeps running until it exits, you stop it, or the service shuts down, so stop jobs you no longer need.`

//nolint:lll // Parameter descriptions stay on one line.
const signalJobSchema = `{
  "type": "object",
  "properties": {
    "jobId": {
      "type": "string",
      "format": "uuid",
      "description": "The job to stop, from runCommand or listJobs."
    },
    "signal": {
      "type": "string",
      "enum": ["stop", "kill"],
      "description": "stop asks the job to exit, then forces it. kill forces it at once."
    }
  },
  "required": ["jobId"],
  "additionalProperties": false
}`
