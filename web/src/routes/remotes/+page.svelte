<script lang="ts">
	import { get, post, del, errorMessage } from '#lib/api.ts';
	import { isAdmin } from '#lib/session.svelte.ts';
	import type { RemoteSummary } from '#lib/types.ts';

	let remotes = $state<RemoteSummary[]>([]);
	let tests = $state<Record<string, { ok: boolean; latencyMs: number; error?: string } | 'pending'>>({});
	let error = $state('');

	async function load() {
		try {
			remotes = (await get<{ items: RemoteSummary[] }>('/remotes')).items;
		} catch (e) {
			error = errorMessage(e);
		}
	}
	void load();

	async function test(n: string) {
		tests[n] = 'pending';
		try {
			tests[n] = await post(`/remotes/${encodeURIComponent(n)}/test`);
		} catch (e) {
			tests[n] = { ok: false, latencyMs: 0, error: errorMessage(e) };
		}
	}
	async function remove(n: string) {
		if (!confirm(`Supprimer le remote « ${n} » ?`)) return;
		try {
			await del('/remotes/' + encodeURIComponent(n));
			await load();
		} catch (e) {
			error = errorMessage(e);
		}
	}
</script>

<svelte:head><title>Remotes · RcsUI</title></svelte:head>

<div class="row">
	<h1>Remotes rclone</h1>
	<span class="spacer"></span>
	{#if isAdmin()}<a class="btn primary" href="/remotes/new">Nouveau remote</a>{/if}
</div>
{#if error}<div class="alert err">{error}</div>{/if}

{#if remotes.length === 0}
	<div class="card empty">Aucun remote{isAdmin() ? '' : ' accessible'}.</div>
{:else}
	<div class="table-wrap">
		<table>
			<thead><tr><th>Nom</th><th>Type</th><th>Test</th><th></th></tr></thead>
			<tbody>
				{#each remotes as r (r.name)}
					{@const t = tests[r.name]}
					<tr>
						<td>{#if isAdmin()}<a href="/remotes/{encodeURIComponent(r.name)}">{r.name}</a>{:else}{r.name}{/if}</td>
						<td class="mono">{r.type}</td>
						<td class="small">
							{#if t === 'pending'}<span class="muted">test…</span>
							{:else if t?.ok}<span class="ok">OK · {t.latencyMs} ms</span>
							{:else if t}<span class="ko" title={t.error}>Échec : {t.error}</span>{/if}
						</td>
						<td class="actions">
							<button onclick={() => test(r.name)} disabled={t === 'pending'}>Tester</button>
							{#if isAdmin()}<button class="danger" onclick={() => remove(r.name)}>Supprimer</button>{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}

<style>
	.ok {
		color: var(--ok);
	}
	.ko {
		color: var(--err);
	}
	.actions {
		text-align: right;
		white-space: nowrap;
	}
</style>
