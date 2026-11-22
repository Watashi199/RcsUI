<script lang="ts">
	import { get, post, del, errorMessage } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import { dateTime, relative } from '#lib/format.ts';
	import { t } from '#lib/i18n/i18n.svelte.ts';
	import type { ApiToken, Role } from '#lib/types.ts';

	const levels: Record<Role, number> = { viewer: 1, operator: 2, admin: 3 };
	const roles = $derived((['viewer', 'operator', 'admin'] as Role[]).filter((r) => levels[r] <= levels[session.user?.role ?? 'viewer']));

	let tokens = $state<ApiToken[]>([]);
	let error = $state('');
	let msg = $state('');
	let created = $state('');
	let nt = $state({ name: '', role: (session.user?.role ?? 'viewer') as Role, expiresInDays: 90 });

	async function load() {
		try {
			tokens = (await get<{ items: ApiToken[] }>('/auth/tokens')).items;
		} catch (e) {
			error = errorMessage(e);
		}
	}
	void load();

	async function create(e: SubmitEvent) {
		e.preventDefault();
		error = msg = created = '';
		try {
			const r = await post<ApiToken & { token: string }>('/auth/tokens', nt);
			created = r.token;
			nt = { ...nt, name: '' };
			await load();
		} catch (err) {
			error = errorMessage(err);
		}
	}

	async function revoke(tk: ApiToken) {
		if (!confirm(t('admin.tokens.revokeConfirm', { name: tk.name }))) return;
		error = msg = '';
		try {
			await del('/auth/tokens/' + tk.id);
			msg = t('admin.tokens.revoked');
			await load();
		} catch (err) {
			error = errorMessage(err);
		}
	}
</script>

<svelte:head><title>{t('admin.tokens.title')} · RcsUI</title></svelte:head>

<h1>{t('admin.tokens.title')}</h1>
<p class="small muted">{t('admin.tokens.help')}</p>
{#if error}<div class="alert err" role="alert">{error}</div>{/if}
{#if msg}<div class="alert ok">{msg}</div>{/if}
{#if created}
	<div class="alert warn">
		{t('admin.tokens.copyNow')}
		<input class="mono" readonly value={created} aria-label={t('admin.tokens.title')} onfocus={(e) => e.currentTarget.select()} />
	</div>
{/if}

<div class="table-wrap">
	<table>
		<thead>
			<tr>
				<th>{t('admin.tokens.name')}</th>
				<th>{t('admin.tokens.role')}</th>
				<th class="hide-sm">{t('admin.tokens.created')}</th>
				<th class="hide-sm">{t('admin.tokens.lastUsed')}</th>
				<th>{t('admin.tokens.expiresAt')}</th>
				<th></th>
			</tr>
		</thead>
		<tbody>
			{#each tokens as tk (tk.id)}
				<tr>
					<td>{tk.name} <span class="mono small muted">{tk.prefix}…</span></td>
					<td>{tk.role}</td>
					<td class="hide-sm small">{dateTime(tk.createdAt)}</td>
					<td class="hide-sm small muted">{tk.lastUsedAt ? relative(tk.lastUsedAt) : '—'}</td>
					<td class="small">{tk.expiresAt ? dateTime(tk.expiresAt) : t('admin.tokens.never')}</td>
					<td class="actions"><button class="danger" onclick={() => revoke(tk)}>{t('admin.tokens.revoke')}</button></td>
				</tr>
			{:else}
				<tr><td colspan="6" class="muted">{t('admin.tokens.none')}</td></tr>
			{/each}
		</tbody>
	</table>
</div>

<h2>{t('admin.tokens.new')}</h2>
<form class="card new" onsubmit={create}>
	<div class="grid2">
		<div class="field">
			<label for="tn">{t('admin.tokens.name')}</label>
			<input id="tn" bind:value={nt.name} required maxlength="64" />
		</div>
		<div class="field">
			<label for="tr">{t('admin.tokens.role')}</label>
			<select id="tr" bind:value={nt.role}>
				{#each roles as r (r)}<option value={r}>{r}</option>{/each}
			</select>
		</div>
		<div class="field">
			<label for="te">{t('admin.tokens.expires')}</label>
			<input id="te" type="number" min="0" max="3650" bind:value={nt.expiresInDays} />
		</div>
	</div>
	<button class="primary" type="submit">{t('common.create')}</button>
</form>

<style>
	.actions {
		text-align: right;
	}
	.new {
		max-width: 900px;
	}
	.alert input {
		margin-top: 0.5rem;
		width: 100%;
	}
</style>
