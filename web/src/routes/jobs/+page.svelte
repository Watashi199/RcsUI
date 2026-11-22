<script lang="ts">
	import { goto } from '$app/navigation';
	import { get, post, errorMessage } from '#lib/api.ts';
	import { live } from '#lib/live.svelte.ts';
	import { canCreateJobs } from '#lib/session.svelte.ts';
	import { relative, dateTime } from '#lib/format.ts';
	import type { Endpoint, Job } from '#lib/types.ts';
	import StateBadge from '#lib/components/StateBadge.svelte';
	import { t } from '#lib/i18n/i18n.svelte.ts';

	let jobs = $state<Job[]>([]);
	let error = $state('');
	let loaded = $state(false);

	async function load() {
		try {
			jobs = (await get<{ items: Job[] }>('/jobs')).items;
		} catch (e) {
			error = errorMessage(e);
		} finally {
			loaded = true;
		}
	}
	$effect(() => {
		void live.version;
		void load();
	});

	const ep = (e: Endpoint) => (e.type === 'remote' ? `${e.remote}:${e.path}` : e.path);
	const canRun = (j: Job) => j.permission === 'run' || j.permission === 'edit';

	async function run(j: Job, dryRun: boolean) {
		error = '';
		try {
			const r = await post<{ runId: string }>(`/jobs/${j.id}/run`, { dryRun });
			await goto('/runs/' + r.runId);
		} catch (e) {
			error = errorMessage(e);
		}
	}
</script>

<svelte:head><title>{t('jobs.list.title')} · RcsUI</title></svelte:head>

<div class="row">
	<h1>{t('jobs.list.title')}</h1>
	<span class="spacer"></span>
	{#if canCreateJobs()}<a class="btn primary" href="/jobs/new">{t('jobs.list.new')}</a>{/if}
</div>
{#if error}<div class="alert err">{error}</div>{/if}

{#if loaded && jobs.length === 0}
	<div class="card empty">
		{t('jobs.list.empty')}
		{#if canCreateJobs()}<a href="/jobs/new">{t('jobs.list.createFirst')}</a>{/if}
	</div>
{:else if jobs.length}
	<div class="table-wrap">
		<table>
			<thead>
				<tr>
					<th>{t('jobs.list.col.name')}</th>
					<th class="hide-sm">{t('jobs.list.col.path')}</th>
					<th>{t('jobs.list.col.lastRun')}</th>
					<th class="hide-sm">{t('jobs.list.col.schedule')}</th>
					<th></th>
				</tr>
			</thead>
			<tbody>
				{#each jobs as j (j.id)}
					<tr>
						<td>
							<a href="/jobs/{j.id}"><strong>{j.name}</strong></a>
							<div class="muted small">{j.engine} · {j.mode}</div>
						</td>
						<td class="hide-sm mono small path">{ep(j.source)} → {ep(j.destination)}</td>
						<td>
							{#if j.lastRun}
								<a href="/runs/{j.lastRun.id}"><StateBadge state={j.active ? 'running' : j.lastRun.state} dryRun={j.lastRun.dryRun} /></a>
								<div class="muted small" title={dateTime(j.lastRun.queuedAt)}>{relative(j.lastRun.queuedAt)}</div>
							{:else}<span class="muted small">{t('jobs.list.never')}</span>{/if}
						</td>
						<td class="hide-sm small">
							{#if j.scheduleCron}
								<span class="mono">{j.scheduleCron}</span>
								{#if !j.scheduleEnabled}<span class="muted">{t('jobs.scheduleDisabled')}</span>{:else if j.nextRunAt}<div class="muted">{relative(j.nextRunAt)}</div>{/if}
							{:else}<span class="muted">{t('jobs.list.manual')}</span>{/if}
						</td>
						<td class="actions">
							{#if canRun(j)}
								<button onclick={() => run(j, false)} disabled={j.active}>{t('jobs.action.run')}</button>
								<button onclick={() => run(j, true)} disabled={j.active} title={t('jobs.list.dryRunTitle')}>{t('jobs.action.dryRun')}</button>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}

<style>
	.path {
		max-width: 420px;
		word-break: break-all;
	}
	.actions {
		text-align: right;
		white-space: nowrap;
	}
</style>
