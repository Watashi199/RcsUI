<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { get, post, put, del, qs, errorMessage } from '#lib/api.ts';
	import { live } from '#lib/live.svelte.ts';
	import { canCreateJobs, isAdmin } from '#lib/session.svelte.ts';
	import { bytes, between, dateTime, relative } from '#lib/format.ts';
	import type { Endpoint, Job, Permission, Run, User } from '#lib/types.ts';
	import StateBadge from '#lib/components/StateBadge.svelte';
	import { t, tn } from '#lib/i18n/i18n.svelte.ts';

	const id = $derived(page.params.id);
	let job = $state<Job | null>(null);
	let runs = $state<Run[]>([]);
	let cursor = $state('');
	let error = $state('');

	async function load() {
		try {
			job = await get<Job>('/jobs/' + id);
			const r = await get<{ items: Run[]; nextCursor: string }>('/runs' + qs({ jobId: id, limit: 25 }));
			runs = r.items;
			cursor = r.nextCursor;
		} catch (e) {
			error = errorMessage(e);
		}
	}
	async function more() {
		const r = await get<{ items: Run[]; nextCursor: string }>('/runs' + qs({ jobId: id, limit: 25, cursor }));
		runs = [...runs, ...r.items];
		cursor = r.nextCursor;
	}
	$effect(() => {
		void live.version;
		void id;
		void load();
	});

	async function run(dryRun: boolean, resync = false) {
		error = '';
		if (
			resync &&
			!confirm(t('jobs.detail.resyncConfirm'))
		)
			return;
		try {
			const r = await post<{ runId: string }>(`/jobs/${id}/run`, resync ? { dryRun, resync } : { dryRun });
			await goto('/runs/' + r.runId);
		} catch (e) {
			error = errorMessage(e);
		}
	}
	async function remove() {
		if (!job || !confirm(t('jobs.detail.deleteConfirm', { name: job.name }))) return;
		try {
			await del('/jobs/' + id);
			await goto('/jobs');
		} catch (e) {
			error = errorMessage(e);
		}
	}
	// Droits par job (admin) : view < run < edit.
	let users = $state<User[]>([]);
	let grants = $state<Record<number, Permission>>({});
	let grantsMsg = $state('');
	$effect(() => {
		if (!isAdmin()) return;
		void id;
		(async () => {
			try {
				const [u, g] = await Promise.all([
					get<{ items: User[] }>('/users'),
					get<{ items: { userId: number; permission: Permission }[] }>(`/jobs/${id}/grants`)
				]);
				users = u.items.filter((x) => x.role !== 'admin');
				grants = Object.fromEntries(g.items.map((x) => [x.userId, x.permission]));
			} catch (e) {
				error = errorMessage(e);
			}
		})();
	});
	async function saveGrants() {
		grantsMsg = '';
		try {
			const list = Object.entries(grants)
				.filter(([, p]) => p)
				.map(([u, p]) => ({ userId: Number(u), permission: p }));
			await put(`/jobs/${id}/grants`, list);
			grantsMsg = t('jobs.detail.grantsSaved');
		} catch (e) {
			error = errorMessage(e);
		}
	}

	const ep = (e: Endpoint) =>
		e.type === 'remote'
			? `${e.remote}:${e.path}`
			: e.type === 'ssh'
				? `${e.ssh?.user}@${e.ssh?.host}:${e.path}`
				: e.path + (e.contentsOnly ? '/' : '');
	const canRun = $derived(job?.permission === 'run' || job?.permission === 'edit');
	const canEdit = $derived(job?.permission === 'edit');
	const optionList = $derived(
		job
			? Object.entries(job.options)
					.filter(([k]) => k !== 'v')
					.map(([k, v]) => `${k}=${Array.isArray(v) ? v.join(', ') : typeof v === 'object' ? JSON.stringify(v) : v}`)
			: []
	);
</script>

<svelte:head><title>{job?.name ?? t('jobs.detail.fallbackTitle')} · RcsUI</title></svelte:head>

{#if error}<div class="alert err">{error}</div>{/if}
{#if job}
	<div class="row">
		<h1>{job.name}</h1>
		<span class="spacer"></span>
		{#if canRun}
			<button class="primary" onclick={() => run(false)} disabled={job.active}>{t('jobs.action.run')}</button>
			<button onclick={() => run(true)} disabled={job.active}>{t('jobs.action.dryRun')}</button>
		{/if}
		{#if canEdit && job.mode === 'bisync'}
			<button onclick={() => run(false, true)} disabled={job.active} title={t('jobs.detail.resyncTitle')}>{t('jobs.detail.resync')}</button>
			<button onclick={() => run(true, true)} disabled={job.active}>{t('jobs.detail.resyncDry')}</button>
		{/if}
		{#if canCreateJobs()}
			<a class="btn" href="/jobs/new?from={job.id}">{t('jobs.action.duplicate')}</a>
		{/if}
		{#if canEdit}
			<a class="btn" href="/jobs/{job.id}/edit">{t('common.edit')}</a>
			<button class="danger" onclick={remove} disabled={job.active}>{t('common.delete')}</button>
		{/if}
	</div>

	<div class="card">
		<dl>
			<dt>{t('jobs.detail.engineMode')}</dt>
			<dd>{job.engine} · {job.mode}</dd>
			<dt>{t('jobs.detail.source')}</dt>
			<dd class="mono">{ep(job.source)}</dd>
			<dt>{t('jobs.detail.destination')}</dt>
			<dd>
				<span class="mono">{ep(job.destination)}</span>
				{#if job.exclusiveDestination}<span class="muted"> · {t('jobs.detail.exclusiveDestination')}</span>{/if}
			</dd>
			<dt>{t('jobs.detail.options')}</dt>
			<dd class="mono small">{optionList.length ? optionList.join(' · ') : t('jobs.detail.defaults')}</dd>
			<dt>{t('jobs.detail.schedule')}</dt>
			<dd>
				{#if job.scheduleCron}
					<span class="mono">{job.scheduleCron}</span>
					{job.scheduleEnabled ? '' : t('jobs.scheduleDisabled')}
					{#if job.nextRunAt}<span class="muted"> · {t('jobs.detail.next', { when: relative(job.nextRunAt) })}</span>{/if}
				{:else}{t('jobs.detail.manual')}{/if}
				{#if job.scheduleCron && job.catchUp}<span class="muted"> · {t('jobs.detail.catchUp')}</span>{/if}
			</dd>
			<dt>{t('jobs.detail.retries')}</dt>
			<dd>
				{#if job.retryMax}
					{tn(job.retryMax, 'jobs.detail.retries.one', 'jobs.detail.retries.other', { delay: job.retryDelaySec })}
				{:else}{t('jobs.detail.noRetries')}{/if}
				{#if job.retryAt}<span class="warn-text"> · {t('jobs.detail.retryAt', { when: relative(job.retryAt) })}</span>{/if}
			</dd>
		</dl>
	</div>

	{#if isAdmin() && users.length}
		<h2>{t('jobs.detail.grants')}</h2>
		{#if grantsMsg}<div class="alert ok">{grantsMsg}</div>{/if}
		<div class="card grants">
			{#each users as u (u.id)}
				<div class="row">
					<span class="uname">{u.username} <span class="muted small">({u.role})</span></span>
					<select bind:value={grants[u.id]} style="width:auto" aria-label={t('jobs.detail.grantFor', { user: u.username })}>
						<option value="">{t('jobs.detail.perm.none')}</option>
						<option value="view">{t('jobs.detail.perm.view')}</option>
						{#if u.role === 'operator'}
							<option value="run">{t('jobs.detail.perm.run')}</option>
							<option value="edit">{t('jobs.detail.perm.edit')}</option>
						{/if}
					</select>
				</div>
			{/each}
			<button style="margin-top:0.6rem" onclick={saveGrants}>{t('jobs.detail.saveGrants')}</button>
		</div>
	{/if}

	<h2>{t('jobs.detail.history')}</h2>
	{#if runs.length === 0}
		<div class="card empty">{t('jobs.detail.neverRun')}</div>
	{:else}
		<div class="table-wrap">
			<table>
				<thead>
					<tr><th>{t('jobs.detail.col.start')}</th><th>{t('jobs.detail.col.state')}</th><th>{t('jobs.detail.col.duration')}</th><th class="hide-sm">{t('jobs.detail.col.transferred')}</th><th class="hide-sm">{t('jobs.detail.col.trigger')}</th></tr>
				</thead>
				<tbody>
					{#each runs as r (r.id)}
						<tr>
							<td><a href="/runs/{r.id}">{dateTime(r.queuedAt)}</a></td>
							<td><StateBadge state={r.live?.state ?? r.state} dryRun={r.dryRun} /></td>
							<td>{between(r.startedAt, r.finishedAt)}</td>
							<td class="hide-sm">{bytes(r.bytesDone)} · {tn(r.filesDone, 'jobs.detail.files.one', 'jobs.detail.files.other')}</td>
							<td class="hide-sm muted small">{r.trigger}{r.attempt > 1 ? ` #${r.attempt}` : ''}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
		{#if cursor}<button style="margin-top:0.6rem" onclick={more}>{t('jobs.detail.more')}</button>{/if}
	{/if}
{/if}

<style>
	.warn-text {
		color: var(--warn);
	}
	dl {
		display: grid;
		grid-template-columns: 150px 1fr;
		gap: 0.4rem 1rem;
		margin: 0;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
		word-break: break-all;
	}
	.grants {
		max-width: 520px;
		display: grid;
		gap: 0.3rem;
	}
	.uname {
		min-width: 220px;
	}
</style>
