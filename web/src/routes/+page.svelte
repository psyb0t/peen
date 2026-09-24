<script lang="ts">
	import { onMount } from "svelte";

	import {
		activities,
		liveText,
		liveThinking,
		type AgentActivity,
	} from "$lib/chat/activity";
	import {
		createPeenAPI,
		PeenAPIError,
		requireData,
		type PeenAPI,
	} from "$lib/api/client";
	import type { components } from "$lib/api/generated";
	import {
		logBrowserEvent,
		startBrowserTimer,
		type BrowserLogMetadata,
		type BrowserLogOperation,
	} from "$lib/browser/log";
	import {
		MAX_LIVE_EVENTS,
		SESSION_DETAIL_LIMIT,
		SESSION_ID_HEADER,
		SESSION_PAGE_LIMIT,
	} from "$lib/common/constants";
	import { formatJSON } from "$lib/common/json";
	import { PeenSocket, type PeenSocketEvent, type SocketState } from "$lib/ws/socket";

	const EVENT_TYPE_MESSAGE_COMPLETED = "message.completed";
	const EVENT_TYPE_MESSAGE_FAILED = "message.failed";
	const EVENT_TYPE_USER_MESSAGE_CREATED = "user_message.created";
	const PATH_SEPARATOR = "/";

	type Session = components["schemas"]["Session"];
	type ExecutionProfile = components["schemas"]["ExecutionProfile"];
	type Model = components["schemas"]["Model"];
	type Message = components["schemas"]["Message"];
	type TranscriptEvent = components["schemas"]["TranscriptEvent"];
	type AgentRun = components["schemas"]["AgentRun"];
	type Compaction = components["schemas"]["Compaction"];
	type ModelRun = components["schemas"]["ModelRun"];
	type SessionNotice = components["schemas"]["SessionNotice"];
	type WorkerGeneration = components["schemas"]["WorkerGeneration"];
	type Job = components["schemas"]["Job"];
	type Turn = components["schemas"]["Turn"];
	type SessionProfileDecision = components["schemas"]["SessionProfileDecision"];

	let apiToken = "";
	let api: PeenAPI | undefined;
	let socket: PeenSocket | undefined;
	let socketState: SocketState = "closed";
	let error = "";
	let loading = false;
	let showInspector = false;

	let workspace = "";
	let workspaceProfile = "";
	let reconfigureProfile = "";
	let reconfigureReason = "";
	let selectedSessionID = "";
	let selectedModel = "";
	let messageText = "";

	let sessions: Session[] = [];
	let workspaceRoots: string[] = [];
	let profiles: ExecutionProfile[] = [];
	let models: Model[] = [];
	let selectedSession: Session | undefined;
	let messages: Message[] = [];
	let transcriptEvents: TranscriptEvent[] = [];
	let agents: AgentRun[] = [];
	let compactions: Compaction[] = [];
	let modelRuns: ModelRun[] = [];
	let notices: SessionNotice[] = [];
	let workers: WorkerGeneration[] = [];
	let jobs: Job[] = [];
	let turns: Turn[] = [];
	let profileDecisions: SessionProfileDecision[] = [];
	let liveEvents: PeenSocketEvent[] = [];

	$: selectedLiveEvents = liveEvents.filter((event) =>
		isSelectedSession(event.metadata.sessionId),
	);
	$: selectedActivities = activities(selectedLiveEvents);
	$: selectedLiveText = liveText(selectedLiveEvents);
	$: selectedLiveThinking = liveThinking(selectedLiveEvents);

	onMount(() => () => socket?.close());

	function sessionHeaders(): { "X-Session-ID": string } {
		return { [SESSION_ID_HEADER]: selectedSessionID };
	}

	function controller(): PeenAPI {
		if (api) {
			return api;
		}

		throw new Error("Connect to the controller first.");
	}

	function setError(operation: BrowserLogOperation, cause: unknown): void {
		showError(
			operation,
			cause instanceof Error ? cause.message : "The controller request failed.",
		);
	}

	function showError(operation: BrowserLogOperation, message: string): void {
		error = message;
		logBrowserEvent("ui.error", { operation });
	}

	function failure(cause: unknown, elapsed: () => number): BrowserLogMetadata {
		return {
			duration_ms: elapsed(),
			...(cause instanceof PeenAPIError ? { status: cause.status } : {}),
		};
	}

	function isSelectedSession(sessionID: string | undefined): boolean {
		return sessionID === selectedSessionID;
	}

	function onSocketEvent(event: PeenSocketEvent): void {
		liveEvents = [...liveEvents, event].slice(-MAX_LIVE_EVENTS);
		if (!isSelectedSession(event.metadata.sessionId)) {
			return;
		}

		if (
			event.type === EVENT_TYPE_USER_MESSAGE_CREATED ||
			event.type === EVENT_TYPE_MESSAGE_COMPLETED ||
			event.type === EVENT_TYPE_MESSAGE_FAILED
		) {
			void loadSession();
		}
	}

	async function connect(event: SubmitEvent): Promise<void> {
		event.preventDefault();
		error = "";
		api = createPeenAPI(apiToken);
		socket?.close();
		socket = new PeenSocket({
			onError: (message) => showError("socket", message),
			onEvent: onSocketEvent,
			onState: (state) => {
				socketState = state;
			},
			token: apiToken,
		});
		socket.connect();
		await refreshController();
	}

	async function refreshController(): Promise<void> {
		const elapsed = startBrowserTimer();
		logBrowserEvent("controller.refresh.start");

		try {
			loading = true;
			const connected = controller();
			const [sessionPage, rootList, profileList, modelList] = await Promise.all([
				requireData(
					connected.GET("/sessions", {
						params: { query: { limit: SESSION_PAGE_LIMIT, offset: 0 } },
					}),
				),
				requireData(connected.GET("/workspace-roots")),
				requireData(connected.GET("/execution-profiles")),
				requireData(connected.GET("/models")),
			]);

			sessions = sessionPage.items;
			workspaceRoots = rootList.roots;
			profiles = profileList.items;
			models = modelList.models;
			if (workspace === "") {
				workspace = workspaceRoots[0] ?? "";
			}
			if (selectedSessionID !== "") {
				selectedSession = sessions.find((session) => session.id === selectedSessionID);
			}

			logBrowserEvent("controller.refresh.complete", {
				count: sessions.length,
				duration_ms: elapsed(),
			});
		} catch (cause) {
			logBrowserEvent("controller.refresh.fail", failure(cause, elapsed));
			setError("controller.refresh", cause);
		} finally {
			loading = false;
		}
	}

	async function openWorkspace(event: SubmitEvent): Promise<void> {
		event.preventDefault();
		if (workspace === "") {
			showError("workspace.open", "The controller has no workspace roots to open.");

			return;
		}

		const elapsed = startBrowserTimer();
		logBrowserEvent("workspace.open.start");

		try {
			loading = true;
			error = "";
			const opened = await requireData(
				controller().POST("/sessions/open", {
					body: {
						workspace: workspace.trim(),
						...(workspaceProfile === "" ? {} : { profile: workspaceProfile }),
					},
				}),
			);
			selectedSessionID = opened.session.id;
			logBrowserEvent("workspace.open.complete", {
				duration_ms: elapsed(),
				session_id: opened.session.id,
			});
			await refreshController();
			await loadSession();
		} catch (cause) {
			logBrowserEvent("workspace.open.fail", failure(cause, elapsed));
			setError("workspace.open", cause);
		} finally {
			loading = false;
		}
	}

	async function selectSession(sessionID: string): Promise<void> {
		selectedSessionID = sessionID;
		error = "";
		await loadSession();
	}

	async function loadSession(): Promise<void> {
		if (selectedSessionID === "") {
			return;
		}

		const loadingSessionID = selectedSessionID;
		const elapsed = startBrowserTimer();
		logBrowserEvent("session.load.start", { session_id: loadingSessionID });

		try {
			loading = true;
			const connected = controller();
			const pageParams = {
				header: sessionHeaders(),
				query: { limit: SESSION_DETAIL_LIMIT, offset: 0 },
			};
			const [
				session,
				messagePage,
				eventPage,
				agentPage,
				compactionPage,
				modelRunPage,
				noticePage,
				workerPage,
				jobPage,
				turnPage,
				decisionPage,
			] = await Promise.all([
				requireData(
					connected.GET("/session", { params: { header: sessionHeaders() } }),
				),
				requireData(connected.GET("/messages", { params: pageParams })),
				requireData(connected.GET("/session/events", { params: pageParams })),
				requireData(connected.GET("/session/agents", { params: pageParams })),
				requireData(connected.GET("/session/compactions", { params: pageParams })),
				requireData(connected.GET("/session/model-runs", { params: pageParams })),
				requireData(connected.GET("/session/notices", { params: pageParams })),
				requireData(connected.GET("/session/workers", { params: pageParams })),
				requireData(connected.GET("/session/jobs", { params: pageParams })),
				requireData(connected.GET("/session/turns", { params: pageParams })),
				requireData(
					connected.GET("/session/profile-decisions", { params: pageParams }),
				),
			]);

			if (loadingSessionID !== selectedSessionID) {
				return;
			}

			selectedSession = session;
			messages = messagePage.items;
			transcriptEvents = eventPage.events;
			agents = agentPage.agents;
			compactions = compactionPage.compactions;
			modelRuns = modelRunPage.modelRuns;
			notices = noticePage.notices;
			workers = workerPage.items;
			jobs = jobPage.jobs;
			turns = turnPage.turns;
			profileDecisions = decisionPage.items;
			logBrowserEvent("session.load.complete", {
				count: messages.length,
				duration_ms: elapsed(),
				session_id: loadingSessionID,
			});
		} catch (cause) {
			logBrowserEvent("session.load.fail", {
				...failure(cause, elapsed),
				session_id: loadingSessionID,
			});
			setError("session.load", cause);
		} finally {
			loading = false;
		}
	}

	async function sendMessage(event: SubmitEvent): Promise<void> {
		event.preventDefault();
		if (selectedSessionID === "") {
			showError("message.send", "Open or select a workspace session first.");

			return;
		}
		if (messageText.trim() === "") {
			return;
		}

		logBrowserEvent("message.send.start", {
			model: selectedModel,
			session_id: selectedSessionID,
		});

		try {
			socket?.send(selectedSessionID, messageText.trim(), selectedModel);
			messageText = "";
		} catch (cause) {
			logBrowserEvent("message.send.fail", { session_id: selectedSessionID });
			setError("message.send", cause);
		}
	}

	async function cancelSession(): Promise<void> {
		if (selectedSessionID === "") {
			return;
		}

		const cancelledSessionID = selectedSessionID;
		const elapsed = startBrowserTimer();
		logBrowserEvent("session.cancel.start", { session_id: cancelledSessionID });

		try {
			await requireData(
				controller().POST("/session/cancel", { params: { header: sessionHeaders() } }),
			);
			logBrowserEvent("session.cancel.complete", {
				duration_ms: elapsed(),
				session_id: cancelledSessionID,
			});
			await loadSession();
		} catch (cause) {
			logBrowserEvent("session.cancel.fail", {
				...failure(cause, elapsed),
				session_id: cancelledSessionID,
			});
			setError("session.cancel", cause);
		}
	}

	async function reconfigureSession(event: SubmitEvent): Promise<void> {
		event.preventDefault();
		if (selectedSessionID === "" || reconfigureProfile === "") {
			showError(
				"session.reconfigure",
				"Select a session and an execution profile first.",
			);

			return;
		}
		if (reconfigureReason.trim() === "") {
			showError(
				"session.reconfigure",
				"State why this session needs a different profile.",
			);

			return;
		}

		const reconfiguredSessionID = selectedSessionID;
		const elapsed = startBrowserTimer();
		logBrowserEvent("session.reconfigure.start", { session_id: reconfiguredSessionID });

		try {
			await requireData(
				controller().POST("/session/reconfigure", {
					body: { profile: reconfigureProfile, reason: reconfigureReason.trim() },
					params: { header: sessionHeaders() },
				}),
			);
			reconfigureReason = "";
			logBrowserEvent("session.reconfigure.complete", {
				duration_ms: elapsed(),
				session_id: reconfiguredSessionID,
			});
			await refreshController();
			await loadSession();
		} catch (cause) {
			logBrowserEvent("session.reconfigure.fail", {
				...failure(cause, elapsed),
				session_id: reconfiguredSessionID,
			});
			setError("session.reconfigure", cause);
		}
	}

	function workspaceName(path: string): string {
		const segments = path.split(PATH_SEPARATOR).filter((segment) => segment !== "");

		return segments.at(-1) ?? path;
	}

	function messageLabel(message: Message): string {
		if (message.role === "user") {
			return "You";
		}
		if (message.role === "tool") {
			return "Tool result";
		}

		return "Peen";
	}

	function messageTime(value: string): string {
		const parsed = new Date(value);
		if (Number.isNaN(parsed.getTime())) {
			return value;
		}

		return parsed.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
	}

	function activityData(activity: AgentActivity): string {
		return formatJSON(activity.data);
	}
</script>

<svelte:head>
	<title>Peen</title>
	<meta
		name="description"
		content="The control surface for durable Peen coding sessions."
	/>
</svelte:head>

<main class="app-shell">
	{#if api === undefined}
		<section class="connect-screen" aria-label="Connect to Peen">
			<div class="connect-card">
				<p class="wordmark">peen</p>
				<h1>Code with a durable agent.</h1>
				<p class="muted">
					Open a workspace, keep its history, and watch the work happen.
				</p>
				<form class="connection" onsubmit={connect}>
					<label
						>API token<input
							bind:value={apiToken}
							autocomplete="off"
							type="password"
						/></label
					>
					<button type="submit">Connect</button>
				</form>
				<p class="quiet">The token stays only in this page’s memory.</p>
			</div>
		</section>
	{:else}
		<aside class="session-sidebar" aria-label="Workspaces and sessions">
			<div class="sidebar-topline">
				<p class="wordmark">peen</p>
				<p class:connected={socketState === "open"} class="connection-state">
					<span aria-hidden="true" class="connection-dot"></span>
					{socketState}
				</p>
			</div>

			<form class="workspace-form" onsubmit={openWorkspace}>
				<div>
					<h2>New workspace</h2>
					<p>Choose a directory this controller allows.</p>
				</div>
				<label>
					Workspace directory
					<input
						bind:value={workspace}
						disabled={workspaceRoots.length === 0}
						list="workspace-roots"
						placeholder="/absolute/path/to/project"
						required
					/>
				</label>
				<datalist id="workspace-roots">
					{#each workspaceRoots as root (root)}
						<option value={root}></option>
					{/each}
				</datalist>
				{#if workspaceRoots.length === 0}
					<p>No workspace roots are available from this controller.</p>
				{:else}
					<p>
						Enter an existing directory under an allowed root. Roots appear as you type.
					</p>
				{/if}
				<label>
					Execution profile
					<select bind:value={workspaceProfile}>
						<option value="">Controller default</option>
						{#each profiles as profile (profile.name)}
							<option value={profile.name}>{profile.name} ({profile.kind})</option>
						{/each}
					</select>
				</label>
				<button disabled={loading || workspace.trim() === ""} type="submit"
					>Open chat</button
				>
			</form>

			<div class="session-list-header">
				<h2>Chats</h2>
				<button
					aria-label="Refresh controller"
					class="icon-button"
					disabled={loading}
					onclick={() => void refreshController()}
					type="button">↻</button
				>
			</div>
			{#if sessions.length === 0}
				<p class="empty-sidebar">No workspace chats yet.</p>
			{:else}
				<nav aria-label="Sessions">
					<ul class="sessions">
						{#each sessions as session (session.id)}
							<li>
								<button
									class:active={session.id === selectedSessionID}
									onclick={() => void selectSession(session.id)}
									type="button"
								>
									<span>{workspaceName(session.workspace)}</span><small
										>{session.messageCount} messages · {session.model}</small
									>
								</button>
							</li>
						{/each}
					</ul>
				</nav>
			{/if}
		</aside>

		<section class="chat-panel" aria-label="Workspace chat">
			<header class="chat-header">
				{#if selectedSession === undefined}
					<div>
						<p class="eyebrow">Workspace agent</p>
						<h1>Pick a workspace to start.</h1>
					</div>
				{:else}
					<div>
						<p class="eyebrow">{selectedSession.workspace}</p>
						<h1>{workspaceName(selectedSession.workspace)}</h1>
						<div class="session-facts">
							<span>{selectedSession.model}</span><span
								>{selectedSession.executionProfile ?? "controller default"}</span
							>{#if selectedSession.activeTurn}<span class="working">Working</span>{/if}
						</div>
					</div>
				{/if}
				<div class="header-actions">
					{#if selectedSession !== undefined}<button
							disabled={!selectedSession.activeTurn}
							onclick={() => void cancelSession()}
							type="button">Stop</button
						>{/if}
					<button
						aria-pressed={showInspector}
						class:active={showInspector}
						onclick={() => (showInspector = !showInspector)}
						type="button">Details</button
					>
				</div>
			</header>

			{#if error !== ""}<p class="error" role="alert">{error}</p>{/if}

			{#if selectedSession === undefined}
				<section class="empty-chat">
					<div>
						<p class="eyebrow">Start here</p>
						<h2>Open a workspace chat from the sidebar.</h2>
						<p>Peen resumes the chat for that directory when it already exists.</p>
					</div>
				</section>
			{:else}
				<div class="conversation" aria-live="polite">
					{#if messages.length === 0 && selectedLiveEvents.length === 0}
						<section class="empty-chat">
							<div>
								<p class="eyebrow">Ready</p>
								<h2>What should Peen work on?</h2>
								<p>It will inspect the workspace before making changes.</p>
							</div>
						</section>
					{/if}

					{#each messages as message (message.id)}
						<article
							class:assistant={message.role === "assistant"}
							class:tool={message.role === "tool"}
							class:user={message.role === "user"}
							class="message"
						>
							<div class="message-meta">
								<span>{messageLabel(message)}</span><time datetime={message.createdAt}
									>{messageTime(message.createdAt)}</time
								>
							</div>
							{#if message.content !== ""}<div class="message-content">
									{message.content}
								</div>{/if}
							{#if message.thinking}<details class="thinking-card">
									<summary>Thinking</summary>
									<div>{message.thinking}</div>
								</details>{/if}
							{#if message.toolCalls}<div class="tool-calls">
									{#each message.toolCalls as call (call.id)}<details
											class="activity-card"
										>
											<summary><span>{call.name}</span><small>Requested</small></summary
											>
											<pre>{formatJSON(call.arguments)}</pre>
										</details>{/each}
								</div>{/if}
							{#if message.role === "tool"}<details class="tool-output">
									<summary>{message.isError ? "Tool failed" : "Tool output"}</summary>
									<pre>{message.content}</pre>
								</details>{/if}
						</article>
					{/each}

					{#if selectedLiveThinking !== ""}<details
							class="thinking-card live-thinking"
							open
						>
							<summary>Thinking</summary>
							<div>{selectedLiveThinking}</div>
						</details>{/if}
					{#each selectedActivities as activity (activity.id)}
						{#if activity.tone === "error" || activity.reason}
							<article
								class:error={activity.tone === "error"}
								class:failure-card={activity.tone === "error"}
								class:warning={activity.tone === "warning"}
								class="activity-card diagnostic-card"
								role={activity.tone === "error" ? "alert" : undefined}
							>
								<div class="activity-heading">
									<span>{activity.title}</span>
									{#if activity.code}<code>{activity.code}</code>{/if}
								</div>
								{#if activity.detail}<p class="activity-message">
										{activity.detail}
									</p>{/if}
								{#if activity.reason}<p class="activity-reason">
										{activity.reason}
									</p>{/if}
								{#if activity.data !== undefined}<details class="technical-details">
										<summary>Technical details</summary>
										<pre>{activityData(activity)}</pre>
									</details>{/if}
							</article>
						{:else}
							<details
								class:success={activity.tone === "success"}
								class:warning={activity.tone === "warning"}
								class="activity-card"
							>
								<summary
									><span>{activity.title}</span>{#if activity.detail}<small
											>{activity.detail}</small
										>{/if}</summary
								>{#if activity.data !== undefined}<pre>{activityData(
											activity,
										)}</pre>{/if}
							</details>
						{/if}
					{/each}
					{#if selectedLiveText !== ""}<article class="message assistant draft">
							<div class="message-meta"><span>Peen</span><small>Writing</small></div>
							<div class="message-content">{selectedLiveText}</div>
						</article>{/if}
				</div>

				<form class="composer" onsubmit={sendMessage}>
					<div class="composer-controls">
						<label
							><span>Model</span><select bind:value={selectedModel}
								><option value="">Session default</option
								>{#each models as model (model.name)}<option value={model.name}
										>{model.name}</option
									>{/each}</select
							></label
						>{#if selectedSession.activeTurn}<p>
								Messages join the active turn’s queue.
							</p>{/if}
					</div>
					<label class="composer-input"
						><span class="sr-only">Message</span><textarea
							bind:value={messageText}
							placeholder="Ask Peen to work on this workspace"
							required
							rows="3"></textarea></label
					>
					<div class="composer-footer">
						<p>Peen will use the selected workspace.</p>
						<button disabled={socketState !== "open"} type="submit">Send</button>
					</div>
				</form>
			{/if}
		</section>

		{#if showInspector}
			<aside class="inspector" aria-label="Session details">
				<div class="inspector-heading">
					<div>
						<p class="eyebrow">Session data</p>
						<h2>Everything recorded</h2>
					</div>
					<button
						aria-label="Close details"
						class="icon-button"
						onclick={() => (showInspector = false)}
						type="button">×</button
					>
				</div>
				{#if selectedSession === undefined}
					<p class="muted">Choose a workspace chat to inspect its durable records.</p>
				{:else}
					<details>
						<summary>Session</summary>
						<pre>{formatJSON(selectedSession)}</pre>
					</details>
					<details>
						<summary>Live events ({selectedLiveEvents.length})</summary>
						<pre>{formatJSON(selectedLiveEvents)}</pre>
					</details>
					<details>
						<summary>Protocol events ({transcriptEvents.length})</summary>
						<pre>{formatJSON(transcriptEvents)}</pre>
					</details>
					<details>
						<summary>Turns ({turns.length})</summary>
						<pre>{formatJSON(turns)}</pre>
					</details>
					<details>
						<summary>Model runs ({modelRuns.length})</summary>
						<pre>{formatJSON(modelRuns)}</pre>
					</details>
					<details>
						<summary>Child agents ({agents.length})</summary>
						<pre>{formatJSON(agents)}</pre>
					</details>
					<details>
						<summary>Compactions ({compactions.length})</summary>
						<pre>{formatJSON(compactions)}</pre>
					</details>
					<details>
						<summary>Workers ({workers.length})</summary>
						<pre>{formatJSON(workers)}</pre>
					</details>
					<details>
						<summary>Jobs ({jobs.length})</summary>
						<pre>{formatJSON(jobs)}</pre>
					</details>
					<details>
						<summary>Profile decisions ({profileDecisions.length})</summary>
						<pre>{formatJSON(profileDecisions)}</pre>
					</details>
					<details>
						<summary>Notices ({notices.length})</summary>
						<pre>{formatJSON(notices)}</pre>
					</details>
					<form class="reconfigure" onsubmit={reconfigureSession}>
						<h3>Change execution profile</h3>
						<label
							>Profile<select bind:value={reconfigureProfile}
								><option value="">Choose a profile</option
								>{#each profiles as profile (profile.name)}<option value={profile.name}
										>{profile.name} ({profile.kind})</option
									>{/each}</select
							></label
						>
						<label>Reason<input bind:value={reconfigureReason} required /></label>
						<button disabled={selectedSession.activeTurn} type="submit"
							>Save profile</button
						>
					</form>
				{/if}
			</aside>
		{/if}
	{/if}
</main>
height: 100dvh; padding: 1rem;

<style>
	:global(*) {
		box-sizing: border-box;
	}
	:global(body) {
		background: #101113;
		color: #f5f5f2;
		font-family:
			Inter,
			ui-sans-serif,
			system-ui,
			-apple-system,
			BlinkMacSystemFont,
			"Segoe UI",
			sans-serif;
		margin: 0;
	}
	:global(button),
	:global(input),
	:global(select),
	:global(textarea) {
		font: inherit;
	}
	.app-shell {
		background:
			radial-gradient(circle at 55% -18%, rgb(214 255 79 / 8%), transparent 36rem),
			#101113;
		height: 100dvh;
		overflow: hidden;
	}
	.connect-screen {
		display: grid;
		min-height: 100vh;
		place-items: center;
		padding: 1.5rem;
	}
	.connect-card {
		background: #1a1b1f;
		border: 1px solid #313238;
		border-radius: 1.25rem;
		box-shadow: 0 24px 80px rgb(0 0 0 / 32%);
		max-width: 30rem;
		padding: 2.25rem;
		width: 100%;
	}
	.wordmark {
		color: #d6ff4f;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 1.1rem;
		font-weight: 800;
		letter-spacing: 0.08em;
		margin: 0;
		text-transform: uppercase;
	}
	h1,
	h2,
	h3,
	p {
		margin-top: 0;
	}
	h1 {
		font-size: clamp(1.5rem, 3vw, 2.1rem);
		letter-spacing: -0.04em;
		margin: 1.25rem 0 0.75rem;
	}
	h2,
	h3 {
		letter-spacing: -0.02em;
	}
	.muted,
	.quiet,
	.empty-sidebar,
	.composer-footer p,
	.workspace-form p,
	.session-facts,
	.composer-controls p {
		color: #96989f;
	}
	.quiet {
		font-size: 0.8rem;
		margin: 0.8rem 0 0;
	}
	.connection,
	.workspace-form,
	.reconfigure {
		display: grid;
		gap: 0.8rem;
	}
	.connection {
		margin-top: 1.5rem;
	}
	label {
		color: #c5c7cd;
		display: grid;
		font-size: 0.78rem;
		font-weight: 650;
		gap: 0.42rem;
		letter-spacing: 0.01em;
	}
	input,
	select,
	textarea {
		background: #202126;
		border: 1px solid #3b3d45;
		border-radius: 0.65rem;
		color: inherit;
		outline: none;
		padding: 0.7rem 0.8rem;
		width: 100%;
	}
	input:focus,
	select:focus,
	textarea:focus {
		border-color: #d6ff4f;
		box-shadow: 0 0 0 3px rgb(214 255 79 / 14%);
	}
	button {
		background: #d6ff4f;
		border: 0;
		border-radius: 0.6rem;
		color: #151610;
		cursor: pointer;
		font-weight: 750;
		padding: 0.66rem 0.9rem;
		transition:
			background 140ms ease,
			box-shadow 140ms ease,
			transform 140ms ease;
	}
	button:hover:not(:disabled),
	button.active {
		background: #e4ff8a;
		box-shadow: 0 6px 20px rgb(214 255 79 / 12%);
		transform: translateY(-1px);
	}
	button:disabled {
		cursor: not-allowed;
		opacity: 0.45;
	}
	.session-sidebar {
		background: #17181c;
		border-right: 1px solid #2b2d33;
		height: 100dvh;
		padding: 1.1rem;
	}
	.session-sidebar {
		left: 0;
		overflow-y: auto;
		position: fixed;
		top: 0;
		width: 17.5rem;
	}
	.sidebar-topline,
	.session-list-header,
	.chat-header,
	.header-actions,
	.inspector-heading,
	.message-meta,
	.composer-controls,
	.composer-footer,
	.session-facts,
	.activity-card summary,
	.tool-output summary {
		display: flex;
		gap: 0.75rem;
	}
	.sidebar-topline,
	.session-list-header,
	.chat-header,
	.inspector-heading,
	.message-meta,
	.composer-footer,
	.activity-card summary,
	.tool-output summary {
		align-items: center;
		justify-content: space-between;
	}
	.connection-state {
		align-items: center;
		background: #2c2d32;
		border-radius: 999px;
		color: #aaaeb7;
		display: inline-flex;
		font-size: 0.7rem;
		font-weight: 700;
		margin: 0;
		padding: 0.28rem 0.55rem;
		text-transform: capitalize;
	}
	.connection-dot {
		background: currentcolor;
		border-radius: 50%;
		height: 0.42rem;
		width: 0.42rem;
	}
	.connection-state.connected {
		background: rgb(214 255 79 / 15%);
		color: #d6ff4f;
	}
	.workspace-form {
		background: #202126;
		border: 1px solid #32343a;
		border-radius: 0.9rem;
		margin: 1.25rem 0 1.5rem;
		padding: 0.9rem;
	}
	.workspace-form h2,
	.session-list-header h2,
	.inspector-heading h2 {
		font-size: 0.95rem;
		margin: 0;
	}
	.workspace-form p {
		font-size: 0.76rem;
		line-height: 1.4;
		margin: 0.25rem 0 0;
	}
	.icon-button {
		align-items: center;
		background: #2a2c32;
		color: #d8d9de;
		display: inline-flex;
		font-size: 1.15rem;
		height: 2rem;
		justify-content: center;
		line-height: 1;
		padding: 0;
		width: 2rem;
	}
	.sessions {
		list-style: none;
		margin: 0.8rem 0;
		padding: 0;
	}
	.sessions li + li {
		margin-top: 0.35rem;
	}
	.sessions button {
		background: transparent;
		border-radius: 0.65rem;
		color: #dedfe3;
		display: grid;
		font-weight: 600;
		gap: 0.25rem;
		padding: 0.7rem;
		text-align: left;
		width: 100%;
	}
	.sessions button:hover,
	.sessions button.active {
		background: #2b2d33;
		color: #fff;
	}
	.sessions button.active {
		box-shadow: inset 2px 0 #d6ff4f;
	}
	.sessions small {
		color: #858891;
		font-size: 0.7rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.empty-sidebar {
		font-size: 0.82rem;
		line-height: 1.5;
		margin: 1rem 0;
	}
	.chat-panel {
		display: flex;
		flex-direction: column;
		height: 100dvh;
		margin-left: 17.5rem;
		min-height: 0;
	}
	.chat-header {
		backdrop-filter: blur(18px);
		background: rgb(16 17 19 / 78%);
		border-bottom: 1px solid #292b31;
		gap: 1rem;
		flex: 0 0 auto;
		min-height: 5.4rem;
		padding: 1rem clamp(1.25rem, 3vw, 3rem);
		position: relative;
		z-index: 2;
	}
	.chat-header h1 {
		font-size: 1.15rem;
		margin: 0.1rem 0 0;
	}
	.eyebrow {
		color: #92959e;
		font-size: 0.73rem;
		font-weight: 650;
		letter-spacing: 0.04em;
		margin: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.header-actions button {
		background: #2a2c32;
		color: #dfe0e3;
		font-size: 0.8rem;
	}
	.header-actions button:hover:not(:disabled),
	.header-actions button.active {
		background: #393c44;
	}
	.session-facts {
		font-size: 0.72rem;
		margin-top: 0.38rem;
	}
	.session-facts span:not(.working) {
		background: #202126;
		border: 1px solid #30323a;
		border-radius: 999px;
		color: #b6bac2;
		padding: 0.18rem 0.45rem;
	}
	.working {
		align-items: center;
		color: #d6ff4f;
		display: inline-flex;
		font-weight: 700;
	}
	.error {
		background: #3d2229;
		border-bottom: 1px solid #70404b;
		color: #ffd5dc;
		margin: 0;
		padding: 0.8rem clamp(1.25rem, 3vw, 3rem);
		flex: 0 0 auto;
	}
	.conversation {
		display: grid;
		flex: 1 1 auto;
		gap: 1rem;
		margin: 0;
		overflow-y: auto;
		padding: 2.2rem clamp(1.25rem, 5vw, 3.5rem);
		width: 100%;
	}
	.empty-chat {
		display: grid;
		min-height: 45vh;
		place-items: center;
		text-align: center;
	}
	.empty-chat > div {
		max-width: 28rem;
	}
	.empty-chat h2 {
		font-size: 1.45rem;
		letter-spacing: -0.04em;
		margin: 0.5rem 0;
	}
	.empty-chat p:not(.eyebrow) {
		color: #96989f;
		line-height: 1.6;
	}
	.message {
		background: rgb(34 35 41 / 82%);
		border: 1px solid rgb(59 61 69 / 82%);
		border-radius: 0.95rem;
		box-shadow: 0 8px 24px rgb(0 0 0 / 10%);
		justify-self: start;
		max-width: min(100%, 44rem);
		padding: 0.9rem 1rem;
	}
	.message.assistant {
		background: transparent;
		border: 0;
		border-radius: 0;
		justify-self: stretch;
		padding: 0.4rem 0;
	}
	.message.user {
		border-color: rgb(214 255 79 / 24%);
	}
	.message.tool {
		background: #1d1e23;
		border-color: #35373e;
	}
	.message-meta {
		color: #989ba4;
		font-size: 0.72rem;
		font-weight: 700;
		margin-bottom: 0.55rem;
		text-transform: uppercase;
	}
	.message-meta time,
	.message-meta small {
		font-size: 0.72rem;
		font-weight: 500;
		text-transform: none;
	}
	.message-content,
	.thinking-card > div {
		line-height: 1.6;
		white-space: pre-wrap;
		word-break: break-word;
	}
	.thinking-card,
	.activity-card,
	.tool-output {
		background: rgb(29 31 36 / 88%);
		border: 1px solid #32353c;
		border-radius: 0.7rem;
		color: #c8cbd0;
		font-size: 0.82rem;
		padding: 0.65rem 0.75rem;
	}
	.thinking-card,
	.tool-output,
	.tool-calls {
		margin-top: 0.75rem;
	}
	.live-thinking {
		border-color: #5e6840;
	}
	.tool-calls {
		display: grid;
		gap: 0.5rem;
	}
	.activity-card {
		margin: 0;
	}
	.activity-card.success {
		border-color: #3d694f;
	}
	.activity-card.warning {
		border-color: #775f30;
	}
	.activity-card.error {
		border-color: #79424d;
	}
	.diagnostic-card {
		max-width: min(100%, 48rem);
		padding: 0.9rem 1rem;
	}
	.diagnostic-card.warning {
		background: #292116;
		border-color: #8b7039;
		color: #fff0ce;
	}
	.failure-card {
		background: #2d1c22;
		border-color: #8d4a58;
		color: #ffdde3;
		max-width: min(100%, 48rem);
		padding: 0.9rem 1rem;
	}
	.activity-heading {
		align-items: center;
		display: flex;
		font-size: 0.84rem;
		font-weight: 800;
		gap: 0.65rem;
		justify-content: space-between;
	}
	.activity-heading code {
		color: #f5b7c1;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 0.68rem;
		font-weight: 650;
	}
	.activity-message {
		font-weight: 650;
		line-height: 1.5;
		margin: 0.65rem 0 0;
	}
	.activity-reason {
		color: #f0bfc8;
		font-size: 0.82rem;
		line-height: 1.5;
		margin: 0.35rem 0 0;
		white-space: pre-wrap;
	}
	.diagnostic-card.warning .activity-reason {
		color: #ead5a1;
	}
	.technical-details {
		border-top: 1px solid rgb(255 221 227 / 18%);
		font-size: 0.78rem;
		margin-top: 0.8rem;
		padding-top: 0.65rem;
	}
	summary {
		cursor: pointer;
		font-weight: 700;
		list-style: none;
	}
	summary::-webkit-details-marker {
		display: none;
	}
	.activity-card summary small {
		color: #96999f;
		font-weight: 500;
	}
	pre {
		background: #15161a;
		border-radius: 0.5rem;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 0.75rem;
		margin: 0.65rem 0 0;
		overflow: auto;
		padding: 0.7rem;
		white-space: pre-wrap;
		word-break: break-word;
	}
	.draft {
		border-left: 2px solid #d6ff4f;
		padding-left: 0.9rem;
	}
	.composer {
		backdrop-filter: blur(12px);
		background: linear-gradient(transparent, rgb(16 17 19 / 92%) 32%);
		flex: 0 0 auto;
		margin-left: 0;
		padding: 1.25rem clamp(1.25rem, 5vw, 3.5rem);
		position: relative;
		width: 100%;
	}
	.composer > * {
		margin: 0 auto;
		max-width: 52rem;
	}
	.composer-input textarea {
		background: #222329;
		border-color: #3a3c43;
		min-height: 5.25rem;
		resize: vertical;
	}
	.composer-controls {
		align-items: center;
		justify-content: space-between;
		margin-bottom: 0.55rem;
	}
	.composer-controls label {
		align-items: center;
		grid-template-columns: auto minmax(9rem, 15rem);
	}
	.composer-controls select {
		font-size: 0.78rem;
		padding: 0.42rem 0.6rem;
	}
	.composer-controls p,
	.composer-footer p {
		font-size: 0.75rem;
		margin: 0;
	}
	.composer-footer {
		margin-top: 0.55rem;
	}
	.composer-footer button {
		min-width: 5rem;
	}
	.inspector {
		background: #17181c;
		border-left: 1px solid #2b2d33;
		border-radius: 1.25rem 0 0 1.25rem;
		box-shadow: -20px 0 60px rgb(0 0 0 / 30%);
		right: 0;
		overflow-y: auto;
		position: fixed;
		top: 0;
		width: min(25rem, 38vw);
		z-index: 3;
	}
	.inspector-heading {
		margin-bottom: 1.25rem;
	}
	.inspector details {
		border-top: 1px solid #30323a;
		padding: 0.75rem 0;
	}
	.inspector details pre {
		max-height: 16rem;
	}
	.reconfigure {
		border-top: 1px solid #30323a;
		margin-top: 0.75rem;
		padding-top: 1rem;
	}
	.reconfigure h3 {
		font-size: 0.9rem;
		margin: 0;
	}
	.sr-only {
		height: 1px;
		margin: -1px;
		overflow: hidden;
		padding: 0;
		position: absolute;
		width: 1px;
	}
	@media (max-width: 900px) {
		.app-shell {
			height: auto;
			overflow: visible;
		}
		.session-sidebar {
			height: auto;
			position: static;
			width: 100%;
		}
		.chat-panel {
			height: 100dvh;
			margin-left: 0;
			width: 100%;
		}
		.inspector {
			box-shadow: -18px 0 60px rgb(0 0 0 / 35%);
			width: min(27rem, 92vw);
		}
	}
	@media (max-width: 600px) {
		.chat-header,
		.composer-controls,
		.composer-footer {
			align-items: flex-start;
			flex-direction: column;
		}
		.header-actions {
			width: 100%;
		}
		.header-actions button {
			flex: 1;
		}
		.composer-controls label {
			grid-template-columns: 1fr;
			width: 100%;
		}
		.composer-controls select {
			width: 100%;
		}
		.composer-footer {
			gap: 0.65rem;
		}
	}
</style>
