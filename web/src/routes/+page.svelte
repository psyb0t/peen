<script lang="ts">
	import { onMount } from "svelte";

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

	let workspace = "";
	let executionProfile = "";
	let reconfigureReason = "";
	let selectedSessionID = "";
	let selectedModel = "";
	let messageText = "";

	let sessions: Session[] = [];
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

	onMount(() => {
		return () => socket?.close();
	});

	function sessionHeaders(): { "X-Session-ID": string } {
		return { [SESSION_ID_HEADER]: selectedSessionID };
	}

	function controller(): PeenAPI {
		if (api) {
			return api;
		}

		throw new Error("Connect to the controller first.");
	}

	// The console records only which operation failed. The error text stays in
	// the page, because it can carry controller or model output.
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
		liveEvents = [event, ...liveEvents].slice(0, MAX_LIVE_EVENTS);
		if (
			isSelectedSession(event.metadata.sessionId) &&
			(event.type === "message.completed" || event.type === "message.failed")
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
			onError: (message) => {
				showError("socket", message);
			},
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
			const [sessionPage, profileList, modelList] = await Promise.all([
				requireData(
					connected.GET("/sessions", {
						params: {
							query: { limit: SESSION_PAGE_LIMIT, offset: 0 },
						},
					}),
				),
				requireData(connected.GET("/execution-profiles")),
				requireData(connected.GET("/models")),
			]);

			sessions = sessionPage.items;
			profiles = profileList.items;
			models = modelList.models;
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
		if (workspace.trim() === "") {
			showError("workspace.open", "Enter an absolute workspace path.");

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
						...(executionProfile === "" ? {} : { profile: executionProfile }),
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
				controller().POST("/session/cancel", {
					params: { header: sessionHeaders() },
				}),
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
		if (selectedSessionID === "" || executionProfile === "") {
			showError(
				"session.reconfigure",
				"Select a session and an execution profile first.",
			);

			return;
		}

		if (reconfigureReason.trim() === "") {
			showError(
				"session.reconfigure",
				"State why this session needs a different execution profile.",
			);

			return;
		}

		const reconfiguredSessionID = selectedSessionID;
		const elapsed = startBrowserTimer();
		logBrowserEvent("session.reconfigure.start", {
			session_id: reconfiguredSessionID,
		});

		try {
			await requireData(
				controller().POST("/session/reconfigure", {
					body: {
						profile: executionProfile,
						reason: reconfigureReason.trim(),
					},
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
</script>

<svelte:head>
	<title>Peen control surface</title>
	<meta
		name="description"
		content="A local control surface for durable Peen coding sessions."
	/>
</svelte:head>

<main>
	<header>
		<div>
			<p class="eyebrow">Peen control surface</p>
			<h1>Durable coding sessions, without hiding the machinery.</h1>
		</div>
		<p class:connected={socketState === "open"} class="connection-state">
			Socket: {socketState}
		</p>
	</header>

	<form class="connection" onsubmit={connect}>
		<label>
			API token
			<input bind:value={apiToken} autocomplete="off" type="password" />
		</label>
		<button type="submit">Connect</button>
		<p>The token stays only in this page’s memory.</p>
	</form>

	{#if error !== ""}
		<p class="error" role="alert">{error}</p>
	{/if}

	{#if api !== undefined}
		<section class="workspace-controls" aria-label="Workspace controls">
			<form onsubmit={openWorkspace}>
				<label>
					Workspace
					<input
						bind:value={workspace}
						placeholder="/absolute/path/to/project"
						required
					/>
				</label>
				<label>
					Profile for a new session
					<select bind:value={executionProfile}>
						<option value="">Controller default</option>
						{#each profiles as profile (profile.name)}
							<option value={profile.name}>
								{profile.name} ({profile.kind})
							</option>
						{/each}
					</select>
				</label>
				<button type="submit">Open workspace</button>
			</form>
			<button disabled={loading} onclick={() => void refreshController()} type="button">
				Refresh controller
			</button>
		</section>

		<div class="layout">
			<aside>
				<h2>Sessions</h2>
				{#if sessions.length === 0}
					<p>No workspaces have been opened yet.</p>
				{:else}
					<ul class="sessions">
						{#each sessions as session (session.id)}
							<li>
								<button
									class:active={session.id === selectedSessionID}
									onclick={() => void selectSession(session.id)}
									type="button"
								>
									<span>{session.workspace}</span>
									<small>{session.model} · {session.messageCount} messages</small>
								</button>
							</li>
						{/each}
					</ul>
				{/if}
			</aside>

			<section class="session-view">
				{#if selectedSession === undefined}
					<p class="empty">Open a workspace or choose an existing session.</p>
				{:else}
					<div class="session-heading">
						<div>
							<p class="eyebrow">{selectedSession.id}</p>
							<h2>{selectedSession.workspace}</h2>
							<p>
								{selectedSession.model} · {selectedSession.executionProfile ??
									"default profile"}
							</p>
						</div>
						<div class="actions">
							<button
								disabled={loading}
								onclick={() => void loadSession()}
								type="button"
							>
								Refresh session
							</button>
							<button
								disabled={!selectedSession.activeTurn}
								onclick={() => void cancelSession()}
								type="button"
							>
								Cancel turn
							</button>
						</div>
					</div>

					<form class="message-form" onsubmit={sendMessage}>
						<label>
							One-turn model override
							<select bind:value={selectedModel}>
								<option value="">Session default</option>
								{#each models as model (model.name)}
									<option value={model.name}>
										{model.name} · {model.contextWindowTokens.toLocaleString()} tokens
									</option>
								{/each}
							</select>
						</label>
						<label>
							Message
							<textarea bind:value={messageText} required rows="5"></textarea>
						</label>
						<button disabled={socketState !== "open"} type="submit">Send message</button
						>
					</form>

					<form class="reconfigure" onsubmit={reconfigureSession}>
						<label>
							Execution profile
							<select bind:value={executionProfile}>
								<option value="">Choose a profile</option>
								{#each profiles as profile (profile.name)}
									<option value={profile.name}>
										{profile.name} ({profile.kind})
									</option>
								{/each}
							</select>
						</label>
						<label>
							Reason
							<input bind:value={reconfigureReason} required />
						</label>
						<button disabled={selectedSession.activeTurn} type="submit"
							>Reconfigure</button
						>
					</form>

					<h3>Transcript</h3>
					{#if messages.length === 0}
						<p class="empty">No durable messages yet.</p>
					{:else}
						<ol class="transcript">
							{#each messages as message (message.id)}
								<li
									class:assistant={message.role === "assistant"}
									class:tool={message.role === "tool"}
								>
									<p>{message.role} · {message.createdAt}</p>
									<pre>{message.content}</pre>
								</li>
							{/each}
						</ol>
					{/if}

					<section class="durable-records" aria-label="Durable session records">
						<h3>Everything Peen has recorded</h3>
						<details>
							<summary>Session ({selectedSession.id})</summary>
							<pre>{formatJSON(selectedSession)}</pre>
						</details>
						<details>
							<summary
								>Live events for this session ({selectedLiveEvents.length})</summary
							>
							<pre>{formatJSON(selectedLiveEvents)}</pre>
						</details>
						<details>
							<summary>Durable protocol events ({transcriptEvents.length})</summary>
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
					</section>
				{/if}
			</section>
		</div>

		<details class="global-events">
			<summary>Global live event stream ({liveEvents.length})</summary>
			<pre>{formatJSON(liveEvents)}</pre>
		</details>
	{/if}
</main>

<style>
	:global(*) {
		box-sizing: border-box;
	}

	:global(body) {
		background: #101416;
		color: #f1f4f2;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		margin: 0;
	}

	main {
		margin: 0 auto;
		max-width: 1500px;
		padding: 2rem;
	}

	header,
	.connection,
	.workspace-controls,
	.session-heading,
	.actions,
	.message-form,
	.reconfigure {
		display: flex;
		gap: 1rem;
	}

	header,
	.session-heading {
		align-items: flex-start;
		justify-content: space-between;
	}

	h1,
	h2,
	h3,
	p {
		margin-top: 0;
	}

	h1 {
		font-size: clamp(1.6rem, 4vw, 3rem);
		max-width: 24ch;
	}

	.eyebrow,
	small {
		color: #9cb4ab;
	}

	.connection,
	.workspace-controls,
	.reconfigure {
		align-items: end;
		background: #192022;
		border: 1px solid #31413e;
		margin: 1rem 0;
		padding: 1rem;
	}

	.connection p {
		align-self: center;
		color: #9cb4ab;
		margin: 0;
	}

	label {
		display: grid;
		font-size: 0.85rem;
		gap: 0.45rem;
	}

	input,
	select,
	textarea,
	button {
		background: #101416;
		border: 1px solid #506560;
		border-radius: 0.25rem;
		color: inherit;
		font: inherit;
		padding: 0.65rem;
	}

	input,
	textarea {
		min-width: 18rem;
	}

	textarea {
		resize: vertical;
	}

	button {
		background: #1f7062;
		cursor: pointer;
	}

	button:hover,
	button.active {
		background: #2d907d;
	}

	button:disabled {
		cursor: not-allowed;
		opacity: 0.55;
	}

	.connection-state {
		background: #5a3223;
		padding: 0.5rem;
	}

	.connection-state.connected {
		background: #1f7062;
	}

	.error {
		background: #5a3223;
		border: 1px solid #d97a58;
		padding: 1rem;
	}

	.layout {
		display: grid;
		grid-template-columns: minmax(18rem, 0.3fr) 1fr;
		gap: 1rem;
	}

	aside,
	.session-view,
	.global-events {
		background: #192022;
		border: 1px solid #31413e;
		padding: 1rem;
	}

	.sessions,
	.transcript {
		list-style: none;
		margin: 0;
		padding: 0;
	}

	.sessions li + li,
	.transcript li + li {
		margin-top: 0.6rem;
	}

	.sessions button {
		display: grid;
		text-align: left;
		width: 100%;
	}

	.message-form {
		flex-direction: column;
		margin: 1rem 0;
	}

	.transcript li {
		background: #101416;
		border-left: 4px solid #5c8391;
		padding: 0.75rem;
	}

	.transcript li.assistant {
		border-left-color: #4fa77f;
	}

	.transcript li.tool {
		border-left-color: #b69653;
	}

	pre {
		margin: 0;
		overflow: auto;
		white-space: pre-wrap;
		word-break: break-word;
	}

	.durable-records {
		margin-top: 1.5rem;
	}

	details {
		border-top: 1px solid #31413e;
		padding: 0.75rem 0;
	}

	summary {
		cursor: pointer;
	}

	details pre {
		background: #101416;
		margin-top: 0.75rem;
		padding: 0.75rem;
	}

	.global-events {
		margin-top: 1rem;
	}

	.empty {
		color: #9cb4ab;
	}

	@media (max-width: 850px) {
		main {
			padding: 1rem;
		}

		header,
		.connection,
		.workspace-controls,
		.reconfigure,
		.layout {
			display: block;
		}

		.connection > *,
		.workspace-controls > *,
		.reconfigure > * {
			margin-bottom: 0.75rem;
		}

		input,
		textarea {
			min-width: 0;
			width: 100%;
		}
	}
</style>
