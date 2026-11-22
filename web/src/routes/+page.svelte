<script lang="ts">
	import { get, errorMessage } from '#lib/api.ts';
	import { live, activeRuns } from '#lib/live.svelte.ts';
	import { session } from '#lib/session.svelte.ts';
	import { relative, dateTime } from '#lib/format.ts';
	import type { Job, Run } from '#lib/types.ts';
	import StateBadge from '#lib/components/StateBadge.svelte';
	import ProgressBar from '#lib/components/ProgressBar.svelte';
	import { t } from '#lib/i18n/i18n.svelte.ts';

	let jobs = $state<Job[]>([]);
	let recent = $state<Run[]>([]);
	let error = $state('');

	async function load() {
		try {
			const [j, r] = await Promise.all([get<{ items: Job[] }>('/jobs'), get<{ items: Run[] }>('/runs?limit=10')]);
			jobs = j.items;
			recent = r.items;
		} catch (e) {
			error = errorMessage(e);
		}
	}

	// Recharger quand un run change d'état (fin, nouveau run).
	$effect(() => {
		void live.version;
		void load();
	});

	const active = $derived(activeRuns());
	const failures = $derived(recent.filter((r) => r.state === 'failed' || r.state === 'partial').slice(0, 5));
	const upcoming = $derived(
		jobs
			.filter((j) => j.nextRunAt)
			.sort((a, b) => (a.nextRunAt ?? '').localeCompare(b.nextRunAt ?? ''))
			.slice(0, 5)
	);
</script>

<svelte:head><title>{t('runs.dashboard.title')} · RcsUI</title></svelte:head>

<h1>{t('runs.dashboard.title')}</h1>
{#if error}<div class="alert err">{error}</div>{/if}

<h2>{t('runs.dashboard.running')}</h2>
{#if active.length === 0}
	<div class="card empty">{t('runs.dashboard.noRunning')}</div>
{:else}
	<div class="list">
		{#each active as r (r.runId)}
			<a class="card run" href="/runs/{r.runId}">
				<div class="row">
					<strong>{r.jobName}</strong>
					<StateBadge state={r.state} dryRun={r.dryRun} />
				</div>
				<ProgressBar p={r.progress} compact />
			</a>
		{/each}
	</div>
{/if}

<div class="grid2">
	<section>
		<h2>{t('runs.dashboard.recentFailures')}</h2>
		{#if failures.length === 0}
			<div class="card empty">{t('runs.dashboard.noFailures')}</div>
		{:else}
			<div class="table-wrap">
				<table>
					<tbody>
						{#each failures as r (r.id)}
							<tr>
								<td><a href="/runs/{r.id}">{r.jobName}</a></td>
								<td><StateBadge state={r.state} /></td>
								<td class="muted small">{relative(r.finishedAt)}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</section>
	<section>
		<h2>{t('runs.dashboard.upcoming')}</h2>
		{#if upcoming.length === 0}
			<div class="card empty">{t('runs.dashboard.noUpcoming')}</div>
		{:else}
			<div class="table-wrap">
				<table>
					<tbody>
						{#each upcoming as j (j.id)}
							<tr>
								<td><a href="/jobs/{j.id}">{j.name}</a></td>
								<td class="mono">{j.scheduleCron}</td>
								<td class="muted small" title={dateTime(j.nextRunAt)}>{relative(j.nextRunAt)}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</section>
</div>

{#if session.info && session.info.allowedRoots.length === 0}
	<div class="alert warn" style="margin-top:1.5rem">
		{t('runs.dashboard.noRoots', { env: 'RCSUI_ALLOWED_ROOTS' })}
	</div>
{/if}

<style>
	.list {
		display: grid;
		gap: 0.6rem;
	}
	.run {
		color: var(--text);
		display: grid;
		gap: 0.5rem;
	}
	.run:hover {
		text-decoration: none;
		border-color: var(--accent);
	}
</style>
