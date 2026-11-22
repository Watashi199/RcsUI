<script lang="ts">
	import { get, qs, errorMessage } from '#lib/api.ts';
	import { live } from '#lib/live.svelte.ts';
	import { bytes, between, dateTime } from '#lib/format.ts';
	import { ACTIVE_STATES, type Run } from '#lib/types.ts';
	import StateBadge from '#lib/components/StateBadge.svelte';
	import { t, type MessageKey } from '#lib/i18n/i18n.svelte.ts';

	const FILTERS: [string, MessageKey][] = [
		['', 'runs.list.filter.all'],
		[ACTIVE_STATES.join(','), 'runs.list.filter.active'],
		['failed,partial', 'runs.list.filter.failed'],
		['succeeded', 'runs.list.filter.succeeded']
	];
	let filter = $state('');
	let runs = $state<Run[]>([]);
	let cursor = $state('');
	let error = $state('');

	async function load() {
		try {
			const r = await get<{ items: Run[]; nextCursor: string }>('/runs' + qs({ state: filter, limit: 50 }));
			runs = r.items;
			cursor = r.nextCursor;
		} catch (e) {
			error = errorMessage(e);
		}
	}
	async function more() {
		const r = await get<{ items: Run[]; nextCursor: string }>('/runs' + qs({ state: filter, limit: 50, cursor }));
		runs = [...runs, ...r.items];
		cursor = r.nextCursor;
	}
	$effect(() => {
		void live.version;
		void filter;
		void load();
	});

	function progressOf(r: Run): string {
		const p = live.runs[r.id]?.progress;
		if (p && p.bytesTotal) return `${((p.bytesDone / p.bytesTotal) * 100).toFixed(0)} %`;
		return '';
	}
</script>

<svelte:head><title>{t('runs.list.title')} · RcsUI</title></svelte:head>

<div class="row">
	<h1>{t('runs.list.title')}</h1>
	<span class="spacer"></span>
	<div class="row" role="group" aria-label={t('runs.list.filter')}>
		{#each FILTERS as [v, label] (v)}
			<button class:primary={filter === v} onclick={() => (filter = v)}>{t(label)}</button>
		{/each}
	</div>
</div>
{#if error}<div class="alert err">{error}</div>{/if}

{#if runs.length === 0}
	<div class="card empty">{t('runs.list.empty')}</div>
{:else}
	<div class="table-wrap">
		<table>
			<thead>
				<tr><th>{t('runs.list.col.job')}</th><th>{t('runs.list.col.state')}</th><th>{t('runs.list.col.start')}</th><th>{t('runs.list.col.duration')}</th><th class="hide-sm">{t('runs.list.col.transferred')}</th><th class="hide-sm">{t('runs.list.col.errors')}</th></tr>
			</thead>
			<tbody>
				{#each runs as r (r.id)}
					{@const st = live.runs[r.id]?.state ?? r.live?.state ?? r.state}
					<tr>
						<td><a href="/runs/{r.id}">{r.jobName}</a> <span class="muted small">{r.engine}</span></td>
						<td><StateBadge state={st} dryRun={r.dryRun} /> <span class="small muted">{progressOf(r)}</span></td>
						<td class="small">{dateTime(r.queuedAt)}</td>
						<td class="small">{between(r.startedAt, r.finishedAt)}</td>
						<td class="hide-sm small">{bytes(r.bytesDone)} · {t('runs.list.files', { n: r.filesDone })}</td>
						<td class="hide-sm small">{r.errors || ''}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
	{#if cursor}<button style="margin-top:0.6rem" onclick={more}>{t('runs.more')}</button>{/if}
{/if}
