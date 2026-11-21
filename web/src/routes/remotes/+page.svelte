<script lang="ts">
	import { get, post, del, errorMessage } from '#lib/api.ts';
	import { isAdmin } from '#lib/session.svelte.ts';
	import type { RemoteSummary } from '#lib/types.ts';
	import { t } from '#lib/i18n/i18n.svelte.ts';

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
		if (!confirm(t('remotes.list.deleteConfirm', { name: n }))) return;
		try {
			await del('/remotes/' + encodeURIComponent(n));
			await load();
		} catch (e) {
			error = errorMessage(e);
		}
	}
</script>

<svelte:head><title>{t('remotes.list.pageTitle')}</title></svelte:head>

<div class="row">
	<h1>{t('remotes.list.title')}</h1>
	<span class="spacer"></span>
	{#if isAdmin()}<a class="btn primary" href="/remotes/new">{t('remotes.list.new')}</a>{/if}
</div>
{#if error}<div class="alert err">{error}</div>{/if}

{#if remotes.length === 0}
	<div class="card empty">{t(isAdmin() ? 'remotes.list.empty' : 'remotes.list.emptyAccessible')}</div>
{:else}
	<div class="table-wrap">
		<table>
			<thead><tr><th>{t('remotes.list.colName')}</th><th>{t('remotes.list.colType')}</th><th>{t('remotes.list.colTest')}</th><th></th></tr></thead>
			<tbody>
				{#each remotes as r (r.name)}
					{@const res = tests[r.name]}
					<tr>
						<td>{#if isAdmin()}<a href="/remotes/{encodeURIComponent(r.name)}">{r.name}</a>{:else}{r.name}{/if}</td>
						<td class="mono">{r.type}</td>
						<td class="small">
							{#if res === 'pending'}<span class="muted">{t('remotes.list.testing')}</span>
							{:else if res?.ok}<span class="ok">{t('remotes.list.testOk', { ms: res.latencyMs })}</span>
							{:else if res}<span class="ko" title={res.error}>{t('remotes.list.testFailed', { error: res.error ?? '' })}</span>{/if}
						</td>
						<td class="actions">
							<button onclick={() => test(r.name)} disabled={res === 'pending'}>{t('remotes.list.test')}</button>
							{#if isAdmin()}<button class="danger" onclick={() => remove(r.name)}>{t('common.delete')}</button>{/if}
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
