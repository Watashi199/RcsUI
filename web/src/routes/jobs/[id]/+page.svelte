<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { get, post, put, del, qs, errorMessage } from '#lib/api.ts';
	import { live } from '#lib/live.svelte.ts';
	import { isAdmin } from '#lib/session.svelte.ts';
	import { bytes, between, dateTime, relative } from '#lib/format.ts';
	import type { Endpoint, Job, Permission, Run, User } from '#lib/types.ts';
	import StateBadge from '#lib/components/StateBadge.svelte';

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

	async function run(dryRun: boolean) {
		error = '';
		try {
			const r = await post<{ runId: string }>(`/jobs/${id}/run`, { dryRun });
			await goto('/runs/' + r.runId);
		} catch (e) {
			error = errorMessage(e);
		}
	}
	async function remove() {
		if (!job || !confirm(`Supprimer le job « ${job.name} » ? L'historique des exécutions est conservé.`)) return;
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
			grantsMsg = 'Droits enregistrés.';
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
					.map(([k, v]) => `${k}=${Array.isArray(v) ? v.join(', ') : v}`)
			: []
	);
</script>

<svelte:head><title>{job?.name ?? 'Job'} · RcsUI</title></svelte:head>

{#if error}<div class="alert err">{error}</div>{/if}
{#if job}
	<div class="row">
		<h1>{job.name}</h1>
		<span class="spacer"></span>
		{#if canRun}
			<button class="primary" onclick={() => run(false)} disabled={job.active}>Lancer</button>
			<button onclick={() => run(true)} disabled={job.active}>Dry-run</button>
		{/if}
		{#if canEdit}
			<a class="btn" href="/jobs/{job.id}/edit">Modifier</a>
			<button class="danger" onclick={remove} disabled={job.active}>Supprimer</button>
		{/if}
	</div>

	<div class="card">
		<dl>
			<dt>Moteur / mode</dt>
			<dd>{job.engine} · {job.mode}</dd>
			<dt>Source</dt>
			<dd class="mono">{ep(job.source)}</dd>
			<dt>Destination</dt>
			<dd class="mono">{ep(job.destination)}</dd>
			<dt>Options</dt>
			<dd class="mono small">{optionList.length ? optionList.join(' · ') : 'par défaut'}</dd>
			<dt>Planification</dt>
			<dd>
				{#if job.scheduleCron}
					<span class="mono">{job.scheduleCron}</span>
					{job.scheduleEnabled ? '' : '(désactivée)'}
					{#if job.nextRunAt}<span class="muted"> · prochaine {relative(job.nextRunAt)}</span>{/if}
				{:else}manuelle{/if}
				{#if job.scheduleCron && job.catchUp}<span class="muted"> · rattrapage au démarrage</span>{/if}
			</dd>
			<dt>Relances</dt>
			<dd>
				{#if job.retryMax}
					jusqu'à {job.retryMax} relance{job.retryMax > 1 ? 's' : ''}, {job.retryDelaySec} s d'intervalle
				{:else}aucune{/if}
				{#if job.retryAt}<span class="warn-text"> · relance prévue {relative(job.retryAt)}</span>{/if}
			</dd>
		</dl>
	</div>

	{#if isAdmin() && users.length}
		<h2>Droits</h2>
		{#if grantsMsg}<div class="alert ok">{grantsMsg}</div>{/if}
		<div class="card grants">
			{#each users as u (u.id)}
				<div class="row">
					<span class="uname">{u.username} <span class="muted small">({u.role})</span></span>
					<select bind:value={grants[u.id]} style="width:auto" aria-label="Droit de {u.username}">
						<option value="">aucun</option>
						<option value="view">voir</option>
						{#if u.role === 'operator'}
							<option value="run">lancer</option>
							<option value="edit">modifier</option>
						{/if}
					</select>
				</div>
			{/each}
			<button style="margin-top:0.6rem" onclick={saveGrants}>Enregistrer les droits</button>
		</div>
	{/if}

	<h2>Historique</h2>
	{#if runs.length === 0}
		<div class="card empty">Jamais exécuté.</div>
	{:else}
		<div class="table-wrap">
			<table>
				<thead>
					<tr><th>Début</th><th>État</th><th>Durée</th><th class="hide-sm">Transféré</th><th class="hide-sm">Déclencheur</th></tr>
				</thead>
				<tbody>
					{#each runs as r (r.id)}
						<tr>
							<td><a href="/runs/{r.id}">{dateTime(r.queuedAt)}</a></td>
							<td><StateBadge state={r.live?.state ?? r.state} dryRun={r.dryRun} /></td>
							<td>{between(r.startedAt, r.finishedAt)}</td>
							<td class="hide-sm">{bytes(r.bytesDone)} · {r.filesDone} fichiers</td>
							<td class="hide-sm muted small">{r.trigger}{r.attempt > 1 ? ` #${r.attempt}` : ''}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
		{#if cursor}<button style="margin-top:0.6rem" onclick={more}>Plus…</button>{/if}
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
