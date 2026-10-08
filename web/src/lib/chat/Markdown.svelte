<script lang="ts">
	import Markdown from "svelte-exmarkdown";
	import { gfmPlugin } from "svelte-exmarkdown/gfm";

	import { safeLinkHref } from "$lib/chat/markdown";

	// The renderer drops raw HTML from the source, so model output can only
	// produce markdown elements. Links are filtered by protocol, and images are
	// shown as links instead of loaded: an image URL in model output would make
	// the browser fetch any address the text names.
	let { source }: { source: string } = $props();

	const plugins = [gfmPlugin()];
</script>

<div class="markdown">
	<Markdown md={source} {plugins}>
		{#snippet a(props)}
			{@const href = safeLinkHref(props.href)}
			{#if href === undefined}<span class="blocked-link"
					>{@render props.children?.()}</span
				>{:else}<a {href} rel="noopener noreferrer nofollow" target="_blank"
					>{@render props.children?.()}</a
				>{/if}
		{/snippet}
		{#snippet img(props)}
			{@const href = safeLinkHref(props.src)}
			{@const label = props.alt || "image"}
			{#if href === undefined}<span class="blocked-link">{label}</span>{:else}<a
					{href}
					rel="noopener noreferrer nofollow"
					target="_blank">{label}</a
				>{/if}
		{/snippet}
	</Markdown>
</div>

<style>
	.markdown {
		line-height: 1.6;
		overflow-wrap: anywhere;
	}
	.markdown :global(:first-child) {
		margin-top: 0;
	}
	.markdown :global(:last-child) {
		margin-bottom: 0;
	}
	.markdown :global(p),
	.markdown :global(ul),
	.markdown :global(ol),
	.markdown :global(blockquote),
	.markdown :global(table) {
		margin: 0 0 0.75rem;
	}
	.markdown :global(ul),
	.markdown :global(ol) {
		padding-left: 1.4rem;
	}
	.markdown :global(h1),
	.markdown :global(h2),
	.markdown :global(h3),
	.markdown :global(h4) {
		font-size: 1rem;
		margin: 1rem 0 0.5rem;
	}
	.markdown :global(code) {
		background: #15161a;
		border-radius: 0.3rem;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 0.85em;
		padding: 0.1rem 0.3rem;
	}
	.markdown :global(pre) {
		background: #15161a;
		border-radius: 0.5rem;
		margin: 0 0 0.75rem;
		overflow: auto;
		padding: 0.7rem;
	}
	.markdown :global(pre code) {
		background: none;
		padding: 0;
	}
	.markdown :global(blockquote) {
		border-left: 3px solid #3a3d45;
		color: #c8cbd0;
		padding-left: 0.75rem;
	}
	.markdown :global(table) {
		border-collapse: collapse;
		display: block;
		overflow-x: auto;
	}
	.markdown :global(th),
	.markdown :global(td) {
		border: 1px solid #32353c;
		padding: 0.3rem 0.6rem;
	}
	.markdown :global(a) {
		color: #d6ff4f;
	}
	.blocked-link {
		text-decoration: underline dotted;
	}
</style>
