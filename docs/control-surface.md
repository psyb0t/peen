# Control surface

Peen serves its own browser control surface at the controller's root URL. It is embedded in the Peen binary, so there is no frontend service to run. It is one client of the public API, and a terminal client, a bot, or your own application can watch the same live work and use the same stored records. Build another client against the [API reference](http-api.md) when you need a different interface or automation.

## Connect

Open the link `peen` printed, or `http://127.0.0.1:8080`, after Peen starts. If `PEEN_API_TOKEN` is set, enter it in the connection form. The browser keeps it in page memory only. It does not put the token in a URL or browser storage.

## Open a workspace

The sidebar shows the workspace root. Type a workspace inside it, such as `$HOME/work/my-app`, and open the chat. Peen returns its existing session when that directory was opened before, and otherwise creates the one session for that workspace. Open `$HOME/work/other-app` for a second project with its own agent.

## Chat

The composer shows the model and reasoning level the next turn will use, starting from the session's model, and you can change either for that turn before you send the work. The input box spans the full width. Enter sends, and Shift+Enter starts a new line. Your messages show as bubbles on the right. The agent's replies take the full width with no bubble, stream in as the model writes them, and render as Markdown, with each tool call shown as one card next to its result. A message you send while a turn is running shows "Queued. Lands after the current step." until the agent receives it.

## Live feed and Stop

The browser keeps one global event socket. It reconnects automatically when the connection drops and catches up on whatever it missed from the session feed. Selecting a chat only changes what the control surface renders from that socket; it no longer opens a second, session-filtered connection, and losing or closing the socket no longer pauses any session's work. The Stop control still pauses the selected session, reconnecting does not resume work Stop already paused, and stopping one child permanently cancels its unfinished subtree.

The global socket receives the live feed for every session. The control surface keeps that global feed intact, but shows the selected session's events and stored records in its own tab. It also offers cancellation, agent space changes, transcript messages, model runs, child agents, compactions, workers, jobs, and notices.
