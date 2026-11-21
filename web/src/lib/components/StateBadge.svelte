<script lang="ts">
	import { STATE_LABEL, stateTone } from '#lib/format.ts';
	import type { RunState } from '#lib/types.ts';

	let { state, dryRun = false }: { state: RunState; dryRun?: boolean } = $props();
</script>

<span class="badge {stateTone(state)}">{STATE_LABEL[state] ?? state}</span>
{#if dryRun}<span class="badge dry">dry-run</span>{/if}

<style>
	.badge {
		display: inline-block;
		font-size: 0.78rem;
		font-weight: 600;
		padding: 0.08rem 0.5rem;
		border-radius: 999px;
		white-space: nowrap;
	}
	.ok {
		background: var(--ok-bg);
		color: var(--ok);
	}
	.warn {
		background: var(--warn-bg);
		color: var(--warn);
	}
	.err {
		background: var(--err-bg);
		color: var(--err);
	}
	.info {
		background: var(--info-bg);
		color: var(--info);
	}
	.muted,
	.dry {
		background: var(--surface-2);
		color: var(--muted);
	}
	.dry {
		margin-left: 0.3rem;
	}
</style>
