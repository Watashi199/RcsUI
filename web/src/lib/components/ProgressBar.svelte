<script lang="ts">
	import { bytes, speed, duration, percent } from '#lib/format.ts';
	import type { Progress } from '#lib/types.ts';
	import { t, tn } from '#lib/i18n/i18n.svelte.ts';

	let { p, compact = false }: { p: Progress | undefined; compact?: boolean } = $props();
	const pct = $derived(p ? percent(p.bytesDone, p.bytesTotal) : null);
</script>

{#if p}
	<div class="wrap">
		<div class="bar" class:indeterminate={pct === null} role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={pct ?? undefined}>
			<div class="fill" style="width: {pct ?? 100}%"></div>
		</div>
		<div class="meta small">
			<span>{pct === null ? '…' : pct.toFixed(1) + ' %'}</span>
			<span>{bytes(p.bytesDone)}{p.bytesTotal ? ' / ' + bytes(p.bytesTotal) : ''}</span>
			<span>{tn(p.filesTotal || p.filesDone, 'runs.progress.files.one', 'runs.progress.files.other', { n: p.filesDone + (p.filesTotal ? '/' + p.filesTotal : '') })}{p.totalsExact ? '' : t('runs.progress.estimate')}</span>
			{#if !compact}
				<span>{speed(p.speedBps)}</span>
				<span>{t('runs.progress.eta', { eta: duration(p.etaSec) })}</span>
				{#if p.deletes}<span>{t('runs.progress.deletes', { n: p.deletes })}</span>{/if}
				{#if p.errors}<span class="err">{tn(p.errors, 'runs.progress.errors.one', 'runs.progress.errors.other')}</span>{/if}
			{/if}
		</div>
		{#if !compact && p.current?.length}
			<ul class="current mono">
				{#each p.current as f (f.name)}
					<li><span class="name">{f.name}</span> <span class="muted">{f.percent.toFixed(0)} %</span></li>
				{/each}
			</ul>
		{/if}
	</div>
{:else}
	<span class="muted small">—</span>
{/if}

<style>
	.bar {
		height: 8px;
		background: var(--surface-2);
		border-radius: 999px;
		overflow: hidden;
	}
	.fill {
		height: 100%;
		background: var(--accent);
		transition: width 0.4s ease;
	}
	.indeterminate .fill {
		opacity: 0.35;
		animation: pulse 1.4s ease-in-out infinite;
	}
	@keyframes pulse {
		50% {
			opacity: 0.7;
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.fill {
			transition: none;
			animation: none !important;
		}
	}
	.meta {
		display: flex;
		gap: 0.9rem;
		flex-wrap: wrap;
		margin-top: 0.3rem;
		color: var(--muted);
	}
	.err {
		color: var(--err);
	}
	.current {
		list-style: none;
		padding: 0;
		margin: 0.4rem 0 0;
	}
	.current li {
		display: flex;
		justify-content: space-between;
		gap: 1rem;
	}
	.name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
</style>
