<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { onDestroy } from 'svelte';
	import { get, post, qs, errorMessage } from '#lib/api.ts';
	import { Stream } from '#lib/sse.ts';
	import { bytes, between, dateTime } from '#lib/format.ts';
	import { ACTIVE_STATES, type Item, type LogRecord, type Progress, type Run, type RunState, type StateEvent } from '#lib/types.ts';
	import StateBadge from '#lib/components/StateBadge.svelte';
	import ProgressBar from '#lib/components/ProgressBar.svelte';

	const MAX_LINES = 5000;
	const id = $derived(page.params.id ?? '');

	let run = $state<Run | null>(null);
	let runState = $state<RunState>('queued');
	let progress = $state<Progress | undefined>();
	let error = $state('');
	let tab = $state<'logs' | 'items'>('logs');

	// Logs : chargés par offset, puis complétés en direct par SSE.
	let lines = $state<LogRecord[]>([]);
	let logOffset = 0;
	let level = $state('');
	let follow = $state(true);
	let logBox: HTMLElement | undefined = $state();

	let items = $state<Item[]>([]);
	let itemsNext = $state(0);
	let itemsEof = $state(false);
	let itemOp = $state('');

	let stream: Stream | null = null;
	const active = $derived(ACTIVE_STATES.includes(runState));

	async function loadRun() {
		const r = await get<Run>('/runs/' + id);
		run = r;
		runState = r.live?.state ?? r.state;
		progress = r.live?.progress ?? {
			phase: 'finalizing',
			bytesDone: r.bytesDone,
			bytesTotal: r.bytesTotal ?? 0,
			filesDone: r.filesDone,
			filesTotal: r.filesTotal ?? 0,
			totalsExact: true,
			deletes: r.filesDeleted,
			errors: r.errors,
			speedBps: 0
		};
	}

	async function loadLogs(reset = false) {
		if (reset) {
			lines = [];
			logOffset = 0;
		}
		for (let i = 0; i < 50; i++) {
			const r = await get<{ lines: LogRecord[]; next: number; eof: boolean }>(
				`/runs/${id}/logs` + qs({ from: logOffset, limit: 2000, level })
			);
			lines = [...lines, ...r.lines].slice(-MAX_LINES);
			const moved = r.next !== logOffset;
			logOffset = r.next;
			if (!moved || r.lines.length < 2000) break;
		}
		scrollDown();
	}

	async function loadItems(reset = false) {
		if (reset) {
			items = [];
			itemsNext = 0;
		}
		const r = await get<{ items: Item[]; next: number; eof: boolean }>(
			`/runs/${id}/items` + qs({ from: itemsNext, limit: 500, op: itemOp })
		);
		items = [...items, ...r.items];
		itemsNext = r.next;
		itemsEof = r.eof;
	}

	function scrollDown() {
		if (!follow || !logBox) return;
		requestAnimationFrame(() => {
			if (logBox) logBox.scrollTop = logBox.scrollHeight;
		});
	}

	function onScroll() {
		if (!logBox) return;
		follow = logBox.scrollHeight - logBox.scrollTop - logBox.clientHeight < 40;
	}

	const rank: Record<string, number> = { debug: 0, info: 1, warn: 2, error: 3 };

	function openStream() {
		stream?.close();
		stream = new Stream(`/api/v1/runs/${id}/events`, {
			onEvent(type, data) {
				if (type === 'run.state') {
					const ev = data as StateEvent;
					runState = ev.to;
					if (ev.progress) progress = ev.progress;
					if (!ACTIVE_STATES.includes(ev.to)) {
						void loadRun();
						void loadLogs();
						if (tab === 'items') void loadItems(true);
					}
				} else if (type === 'run.progress') {
					progress = data as Progress;
				} else if (type === 'run.log') {
					const b = data as { fromOffset: number; toOffset: number; lines: LogRecord[] };
					if (b.fromOffset !== logOffset) {
						// Lot déjà lu (rejeu) ou trou : relire depuis notre offset.
						if (b.toOffset > logOffset) void loadLogs();
						return;
					}
					const min = rank[level] ?? 0;
					lines = [...lines, ...b.lines.filter((l) => (rank[l.lvl] ?? 1) >= min)].slice(-MAX_LINES);
					logOffset = b.toOffset;
					scrollDown();
				}
			},
			onResync() {
				void loadRun();
				void loadLogs();
			},
			onAuth() {
				void goto('/login');
			}
		});
		stream.open();
	}

	$effect(() => {
		const runId = id;
		if (!runId) return;
		error = '';
		(async () => {
			try {
				await loadRun();
				await loadLogs(true);
				if (ACTIVE_STATES.includes(runState)) openStream();
			} catch (e) {
				error = errorMessage(e);
			}
		})();
		return () => {
			stream?.close();
			stream = null;
		};
	});

	// Onglet caché plus d'une minute : couper le flux, reprendre au retour.
	let hiddenTimer: ReturnType<typeof setTimeout> | null = null;
	function onVisibility() {
		if (document.hidden) {
			hiddenTimer = setTimeout(() => {
				stream?.close();
				stream = null;
			}, 60_000);
		} else {
			if (hiddenTimer) clearTimeout(hiddenTimer);
			if (!stream && active) {
				void loadRun().then(() => loadLogs());
				openStream();
			}
		}
	}
	onDestroy(() => {
		if (hiddenTimer) clearTimeout(hiddenTimer);
		stream?.close();
	});

	async function cancel() {
		if (!confirm('Annuler cette exécution ?')) return;
		try {
			await post(`/runs/${id}/cancel`);
		} catch (e) {
			error = errorMessage(e);
		}
	}
	async function retry() {
		try {
			const r = await post<{ runId: string }>(`/runs/${id}/retry`);
			await goto('/runs/' + r.runId);
		} catch (e) {
			error = errorMessage(e);
		}
	}

	const canRun = $derived(run?.permission === 'run' || run?.permission === 'edit');
	const fmtTime = (t: number) => new Date(t).toLocaleTimeString('fr-FR');
</script>

<svelte:document onvisibilitychange={onVisibility} />
<svelte:head><title>{run?.jobName ?? 'Exécution'} · RcsUI</title></svelte:head>

{#if error}<div class="alert err">{error}</div>{/if}
{#if run}
	<div class="row">
		<h1>{run.jobName}</h1>
		<StateBadge state={runState} dryRun={run.dryRun} />
		<span class="spacer"></span>
		{#if canRun && active}<button class="danger" onclick={cancel} disabled={runState === 'cancelling'}>Annuler</button>{/if}
		{#if canRun && !active && run.jobId}<button onclick={retry}>Relancer</button>{/if}
		{#if run.jobId}<a class="btn" href="/jobs/{run.jobId}">Voir le job</a>{/if}
	</div>

	<div class="card">
		<ProgressBar p={progress} />
		<div class="meta small muted">
			<span>Mis en file : {dateTime(run.queuedAt)}</span>
			<span>Durée : {between(run.startedAt, run.finishedAt)}</span>
			<span>Déclencheur : {run.trigger}{run.attempt > 1 ? ` (tentative ${run.attempt})` : ''}</span>
			{#if run.exitCode !== undefined}<span>Code de sortie : {run.exitCode}</span>{/if}
			<span>Moteur : {run.engine}</span>
		</div>
		{#if run.error && !active}<div class="alert err" style="margin:0.6rem 0 0">{run.error}</div>{/if}
	</div>

	<div class="tabs row">
		<button class:primary={tab === 'logs'} onclick={() => (tab = 'logs')}>Logs</button>
		<button
			class:primary={tab === 'items'}
			onclick={() => {
				tab = 'items';
				void loadItems(true);
			}}
		>
			Changements{run.dryRun ? ' prévus' : ''}
		</button>
		<span class="spacer"></span>
		{#if tab === 'logs'}
			<select bind:value={level} onchange={() => loadLogs(true)} aria-label="Niveau minimal" style="width:auto">
				<option value="">Tous niveaux</option>
				<option value="info">Info+</option>
				<option value="warn">Avertissements+</option>
				<option value="error">Erreurs</option>
			</select>
			<label class="check"><input type="checkbox" bind:checked={follow} onchange={scrollDown} /> Suivre</label>
			<a class="btn" href="/api/v1/runs/{id}/logs.ndjson" download>Télécharger</a>
		{:else}
			<select bind:value={itemOp} onchange={() => loadItems(true)} aria-label="Opération" style="width:auto">
				<option value="">Toutes opérations</option>
				<option value="transfer">Transferts</option>
				<option value="delete">Suppressions</option>
				<option value="create">Créations</option>
				<option value="attrs">Attributs</option>
			</select>
		{/if}
	</div>

	{#if tab === 'logs'}
		<div class="logs mono" bind:this={logBox} onscroll={onScroll} role="log">
			{#each lines as l, i (i)}
				<div class="line {l.lvl}"><span class="t">{fmtTime(l.t)}</span> <span class="s">{l.s}</span> {l.msg}</div>
			{/each}
			{#if lines.length === 0}<div class="muted">Aucun log.</div>{/if}
		</div>
		{#if lines.length >= MAX_LINES}
			<div class="muted small">Seules les {MAX_LINES} dernières lignes sont affichées ; téléchargez le log complet.</div>
		{/if}
	{:else}
		<div class="table-wrap">
			<table class="items">
				<thead><tr><th>Opération</th><th>Chemin</th><th>Taille</th></tr></thead>
				<tbody>
					{#each items as it, i (i)}
						<tr>
							<td class="op {it.op}">{it.op}{it.kind === 'd' ? ' (dossier)' : ''}</td>
							<td class="mono path">{it.path}</td>
							<td class="small">{it.op === 'delete' ? '' : bytes(it.size)}</td>
						</tr>
					{/each}
					{#if items.length === 0}<tr><td colspan="3" class="empty">Aucun changement.</td></tr>{/if}
				</tbody>
			</table>
		</div>
		{#if !itemsEof && items.length}<button style="margin-top:0.6rem" onclick={() => loadItems()}>Plus…</button>{/if}
	{/if}
{/if}

<style>
	.meta {
		display: flex;
		flex-wrap: wrap;
		gap: 1rem;
		margin-top: 0.7rem;
	}
	.tabs {
		margin: 1.2rem 0 0.6rem;
	}
	.logs {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 0.5rem 0.7rem;
		height: 55vh;
		overflow: auto;
		font-size: 0.8rem;
		line-height: 1.5;
	}
	.line {
		white-space: pre-wrap;
		word-break: break-all;
	}
	.line .t,
	.line .s {
		color: var(--muted);
	}
	.line.warn {
		color: var(--warn);
	}
	.line.error {
		color: var(--err);
	}
	.line.debug {
		opacity: 0.7;
	}
	.items .path {
		word-break: break-all;
	}
	.op.delete {
		color: var(--err);
	}
	.op.transfer {
		color: var(--ok);
	}
</style>
