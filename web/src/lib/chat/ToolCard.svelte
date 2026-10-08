<script lang="ts">
	import type { ToolCallView, ToolStatus } from "$lib/chat/transcript";

	let { call }: { call: ToolCallView } = $props();

	const STATUS_LABELS: Record<ToolStatus, string> = {
		done: "Done",
		error: "Error",
		missing: "No result",
		running: "Running",
	};
</script>

<details
	class="tool-card"
	class:error={call.status === "error"}
	class:running={call.status === "running"}
	data-status={call.status}
>
	<summary>
		<span class="tool-name">{call.name}</span>
		<span class="tool-status">{STATUS_LABELS[call.status]}</span>
	</summary>
	<div class="tool-body">
		{#if call.arguments !== ""}
			<p class="tool-label">Arguments</p>
			<pre>{call.arguments}</pre>
		{/if}
		{#if call.result !== undefined}
			<p class="tool-label">{call.status === "error" ? "Error" : "Result"}</p>
			<pre>{call.result}</pre>
		{/if}
	</div>
</details>

<style>
	.tool-card {
		background: rgb(29 31 36 / 88%);
		border: 1px solid #3d694f;
		border-radius: 0.7rem;
		color: #c8cbd0;
		font-size: 0.82rem;
		padding: 0.55rem 0.75rem;
	}
	.tool-card.running {
		border-color: #5e6840;
	}
	.tool-card.error {
		border-color: #8d4a58;
	}
	summary {
		align-items: center;
		cursor: pointer;
		display: flex;
		gap: 0.6rem;
	}
	.tool-name {
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-weight: 650;
	}
	.tool-status {
		color: #8fd6a8;
		font-size: 0.72rem;
		font-weight: 700;
		margin-left: auto;
		text-transform: uppercase;
	}
	.running .tool-status {
		animation: blink 1.2s steps(1) infinite;
		color: #d6ff4f;
	}
	.error .tool-status {
		color: #ff9aa9;
	}
	.tool-label {
		color: #989ba4;
		font-size: 0.7rem;
		font-weight: 700;
		margin: 0.65rem 0 0;
		text-transform: uppercase;
	}
	pre {
		background: #15161a;
		border-radius: 0.5rem;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 0.75rem;
		margin: 0.3rem 0 0;
		max-height: 20rem;
		overflow: auto;
		padding: 0.7rem;
		white-space: pre-wrap;
		word-break: break-word;
	}
	@keyframes blink {
		50% {
			opacity: 0.45;
		}
	}
</style>
