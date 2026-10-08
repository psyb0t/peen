<script lang="ts">
	import Markdown from "$lib/chat/Markdown.svelte";
	import ToolCard from "$lib/chat/ToolCard.svelte";
	import type { ReplyBlock } from "$lib/chat/transcript";

	let { blocks }: { blocks: ReplyBlock[] } = $props();
</script>

<div class="reply-blocks">
	{#each blocks as block (block.key)}
		{#if block.kind === "text"}
			<Markdown source={block.text} />
		{:else if block.kind === "thinking"}
			<details class="note-card thinking">
				<summary>Thinking</summary>
				<div class="note-text">{block.text}</div>
			</details>
		{:else if block.kind === "injected"}
			<details class="note-card injected">
				<summary>Background update</summary>
				<pre>{block.text}</pre>
			</details>
		{:else if block.kind === "tool"}
			<ToolCard call={block} />
		{/if}
	{/each}
</div>

<style>
	.reply-blocks {
		display: grid;
		gap: 0.6rem;
	}
	.note-card {
		background: rgb(29 31 36 / 88%);
		border: 1px solid #32353c;
		border-radius: 0.7rem;
		color: #c8cbd0;
		font-size: 0.82rem;
		padding: 0.55rem 0.75rem;
	}
	.note-card.thinking {
		border-color: #5e6840;
	}
	summary {
		cursor: pointer;
		font-weight: 650;
	}
	.note-text {
		line-height: 1.6;
		margin-top: 0.5rem;
		white-space: pre-wrap;
		word-break: break-word;
	}
	pre {
		background: #15161a;
		border-radius: 0.5rem;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 0.75rem;
		margin: 0.5rem 0 0;
		max-height: 20rem;
		overflow: auto;
		padding: 0.7rem;
		white-space: pre-wrap;
		word-break: break-word;
	}
</style>
