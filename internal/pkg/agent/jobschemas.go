package agent

// Job tool descriptions and schemas. Like the file tools, each description
// carries its own usage rules and bounds so the base system prompt does not
// have to.

const listJobsDescription = `List the commands this session has started.
Covers running jobs and finished ones, with the purpose you gave when starting
each, its state, timings, and how much output is buffered.
Filter by state to narrow it. An empty state lists every job.
Use this when you have lost track of what you started, or after being told a
job finished.`

const listJobsSchema = `{
  "type": "object",
  "properties": {
    "state": {
      "type": "string",
      "enum": ["running", "exited", "signalled", "failed"],
      "description": "Filter to one state. Empty lists every job."
    }
  },
  "additionalProperties": false
}`

const readJobOutputDescription = `Read a bounded window of one job's output
without waiting for it to finish.
Pass the cursor from a previous call to continue where you left off; zero
starts from the oldest line still buffered.
A job's buffer is bounded, so a very chatty process drops its oldest lines and
reports how many in the dropped counts. If you see a non-zero dropped count you
missed output, so read more often or narrow what the command prints.
found is false when the job ID is not one of this session's jobs. That is a
reported result, not an error.`

const readJobOutputSchema = `{
  "type": "object",
  "properties": {
    "jobId": {
      "type": "string",
      "format": "uuid",
      "description": "The job to read, from run_command or list_jobs."
    },
    "stream": {
      "type": "string",
      "enum": ["stdout", "stderr", "both"],
      "description": "Which stream to read. Empty means both."
    },
    "stdoutCursor": {
      "type": "integer",
      "description": "Resume point from a previous call. 0 reads from oldest."
    },
    "stderrCursor": {
      "type": "integer",
      "description": "Resume point from a previous call. 0 reads from oldest."
    },
    "maxLines": {
      "type": "integer",
      "description": "Line bound for this window. 0 uses the configured bound."
    }
  },
  "required": ["jobId"],
  "additionalProperties": false
}`

const waitJobDescription = `Block until one job exits, up to a bound.
Returns the job's final state and exit code plus whatever output arrived while
waiting, continuing from the cursors you pass.
The bound limits how long YOU wait, never how long the job may run. When it
elapses first, running is true and the job is still going; call again or leave
it and carry on with something else.
found is false when the job ID is not one of this session's jobs.`

const waitJobSchema = `{
  "type": "object",
  "properties": {
    "jobId": {
      "type": "string",
      "format": "uuid",
      "description": "The job to wait for."
    },
    "timeoutSeconds": {
      "type": "integer",
      "description": "How long to wait. 0 uses the configured default."
    },
    "stdoutCursor": {
      "type": "integer",
      "description": "Resume point from a previous call. 0 reads from oldest."
    },
    "stderrCursor": {
      "type": "integer",
      "description": "Resume point from a previous call. 0 reads from oldest."
    }
  },
  "required": ["jobId"],
  "additionalProperties": false
}`

const signalJobDescription = `Stop a running job.
stop sends SIGTERM to the whole process group and escalates to SIGKILL if it
does not exit in time. kill goes straight to SIGKILL. Both reach commands that
forked or backgrounded children, so nothing is left orphaned.
Signalling an unknown, already exited, or already signalled job is not an
error: the current state is reported instead.
Clean up jobs you no longer need. A background job keeps running until it
exits, you signal it, or the service shuts down.`

const signalJobSchema = `{
  "type": "object",
  "properties": {
    "jobId": {
      "type": "string",
      "format": "uuid",
      "description": "The job to signal."
    },
    "signal": {
      "type": "string",
      "enum": ["stop", "kill"],
      "description": "stop is graceful then forced. kill is immediate."
    }
  },
  "required": ["jobId"],
  "additionalProperties": false
}`
