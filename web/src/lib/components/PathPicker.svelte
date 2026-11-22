<script lang="ts">
	import { get, qs, errorMessage } from '#lib/api.ts';
	import { bytes } from '#lib/format.ts';
	import type { BrowseEntry } from '#lib/types.ts';
	import { t } from '#lib/i18n/i18n.svelte.ts';

	let {
		loc,
		remote = '',
		start = '',
		onpick,
		onclose
	}: { loc: 'local' | 'remote'; remote?: string; start?: string; onpick: (p: string) => void; onclose: () => void } =
		$props();

	let roots = $state<string[]>([]);
	let path = $state('');
	let entries = $state<BrowseEntry[]>([]);
	let truncated = $state(false);
	let error = $state('');
	let loading = $state(false);
	let dialog: HTMLDialogElement | undefined = $state();

	const sep = '/';
	const join = (a: string, b: string) => (a === '' ? b : a.endsWith(sep) ? a + b : a + sep + b);
	function parent(p: string): string | null {
		if (loc === 'local') {
			if (roots.includes(p)) return null;
			const i = p.lastIndexOf(sep);
			const up = i <= 0 ? sep : p.slice(0, i);
			return roots.some((r) => up === r || up.startsWith(r + sep)) ? up : null;
		}
		if (p === '') return null;
		const i = p.lastIndexOf(sep);
		return i < 0 ? '' : p.slice(0, i);
	}

	async function open(p: string) {
		loading = true;
		error = '';
		try {
			const res = await get<{ path: string; entries: BrowseEntry[]; truncated: boolean }>(
				'/browse' + qs({ loc, remote: loc === 'remote' ? remote : undefined, path: p })
			);
			path = loc === 'local' ? res.path : p;
			entries = res.entries;
			truncated = res.truncated;
		} catch (e) {
			error = errorMessage(e);
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		dialog?.showModal();
	});

	(async () => {
		if (loc === 'local') {
			const r = await get<{ roots: string[] }>('/browse/roots').catch(() => ({ roots: [] }));
			roots = r.roots;
			const initial = start && roots.some((x) => start === x || start.startsWith(x + sep)) ? start : roots[0];
			if (initial) await open(initial);
			else error = t('runs.picker.noRoots');
		} else {
			await open(start);
		}
	})();
</script>

<dialog bind:this={dialog} onclose={onclose} aria-label={t('runs.picker.title')}>
	<div class="head row">
		<strong>{loc === 'local' ? t('runs.picker.local') : t('runs.picker.remote', { remote })}</strong>
		<span class="spacer"></span>
		<button type="button" onclick={() => dialog?.close()} aria-label={t('common.close')}>✕</button>
	</div>
	{#if loc === 'local' && roots.length > 1}
		<div class="row small">
			{t('runs.picker.roots')}
			{#each roots as r (r)}<button type="button" class="chip" onclick={() => open(r)}>{r}</button>{/each}
		</div>
	{/if}
	<div class="path mono">{loc === 'remote' ? remote + ':' : ''}{path || (loc === 'remote' ? '' : '/')}</div>
	{#if error}<div class="alert err">{error}</div>{/if}
	<ul class="entries">
		{#if parent(path) !== null}
			<li><button type="button" class="entry" onclick={() => open(parent(path) ?? '')}>⬑ ..</button></li>
		{/if}
		{#each entries as e (e.name)}
			<li>
				{#if e.dir}
					<button type="button" class="entry" onclick={() => open(join(path, e.name))}>📁 {e.name}{e.symlink ? ' ↪' : ''}</button>
				{:else}
					<span class="entry file">📄 {e.name} <span class="muted small">{bytes(e.size)}</span></span>
				{/if}
			</li>
		{/each}
		{#if !loading && entries.length === 0 && !error}<li class="muted small">{t('runs.picker.empty')}</li>{/if}
	</ul>
	{#if truncated}<div class="muted small">{t('runs.picker.truncated')}</div>{/if}
	<div class="row foot">
		<span class="spacer"></span>
		<button type="button" onclick={() => dialog?.close()}>{t('common.cancel')}</button>
		<button type="button" class="primary" disabled={loading || !!error} onclick={() => { onpick(path); dialog?.close(); }}>
			{t('runs.picker.choose')}
		</button>
	</div>
</dialog>

<style>
	dialog {
		width: min(640px, 95vw);
		max-height: 80vh;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: var(--surface);
		color: var(--text);
		padding: 1rem;
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
	}
	dialog::backdrop {
		background: rgb(0 0 0 / 0.4);
	}
	.path {
		background: var(--surface-2);
		padding: 0.35rem 0.5rem;
		border-radius: 6px;
		word-break: break-all;
	}
	.entries {
		list-style: none;
		margin: 0;
		padding: 0;
		overflow-y: auto;
		flex: 1;
		min-height: 200px;
		border: 1px solid var(--border);
		border-radius: 6px;
	}
	.entry {
		display: block;
		width: 100%;
		text-align: left;
		border: none;
		border-radius: 0;
		padding: 0.3rem 0.6rem;
		background: none;
	}
	.entry.file {
		color: var(--muted);
	}
	.chip {
		padding: 0.1rem 0.5rem;
		font-size: 0.8rem;
	}
</style>
