<script lang="ts">
	import { get, post, patch, del, errorMessage } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import { relative } from '#lib/format.ts';
	import { t } from '#lib/i18n/i18n.svelte.ts';
	import type { Role, User } from '#lib/types.ts';

	let users = $state<User[]>([]);
	let error = $state('');
	let msg = $state('');
	let nu = $state({ username: '', password: '', role: 'operator' as Role });

	async function load() {
		try {
			users = (await get<{ items: User[] }>('/users')).items;
		} catch (e) {
			error = errorMessage(e);
		}
	}
	void load();

	async function act(fn: () => Promise<unknown>, ok: string) {
		error = '';
		msg = '';
		try {
			await fn();
			msg = ok;
			await load();
		} catch (e) {
			error = errorMessage(e);
		}
	}

	function create(e: SubmitEvent) {
		e.preventDefault();
		void act(async () => {
			await post('/users', nu);
			nu = { username: '', password: '', role: 'operator' };
		}, t('admin.users.created'));
	}
	const setRole = (u: User, role: Role) => act(() => patch('/users/' + u.id, { role }), t('admin.users.roleChanged'));
	const toggle = (u: User) =>
		act(() => patch('/users/' + u.id, { disabled: !u.disabled }), u.disabled ? t('admin.users.reactivated') : t('admin.users.deactivated'));
	function reset(u: User) {
		const pw = prompt(t('admin.users.resetPrompt', { name: u.username }));
		if (pw) void act(() => patch('/users/' + u.id, { password: pw }), t('admin.users.passwordReset'));
	}
	function remove(u: User) {
		if (confirm(t('admin.users.deleteConfirm', { name: u.username })))
			void act(() => del('/users/' + u.id), t('admin.users.deleted'));
	}
</script>

<svelte:head><title>{t('admin.users.title')} · RcsUI</title></svelte:head>

<h1>{t('admin.users.title')}</h1>
{#if error}<div class="alert err">{error}</div>{/if}
{#if msg}<div class="alert ok">{msg}</div>{/if}

<div class="table-wrap">
	<table>
		<thead><tr><th>{t('admin.users.user')}</th><th>{t('admin.users.role')}</th><th class="hide-sm">{t('admin.users.lastLogin')}</th><th></th></tr></thead>
		<tbody>
			{#each users as u (u.id)}
				<tr class:disabled={u.disabled}>
					<td>
						{u.username}
						{#if u.disabled}<span class="muted small">{t('admin.users.disabledTag')}</span>{/if}
						{#if u.mustChangePassword}<span class="muted small">{t('admin.users.mustChangeTag')}</span>{/if}
					</td>
					<td>
						<select
							value={u.role}
							onchange={(e) => setRole(u, e.currentTarget.value as Role)}
							disabled={u.id === session.user?.id}
							style="width:auto"
						>
							<option value="admin">admin</option>
							<option value="operator">operator</option>
							<option value="viewer">viewer</option>
						</select>
					</td>
					<td class="hide-sm small muted">{relative(u.lastLoginAt)}</td>
					<td class="actions">
						{#if u.id !== session.user?.id}
							<button onclick={() => reset(u)}>{t('admin.users.reset')}</button>
							<button onclick={() => toggle(u)}>{u.disabled ? t('admin.users.reactivate') : t('admin.users.deactivate')}</button>
							<button class="danger" onclick={() => remove(u)}>{t('common.delete')}</button>
						{/if}
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>

<h2>{t('admin.users.new')}</h2>
<form class="card new" onsubmit={create}>
	<div class="grid2">
		<div class="field">
			<label for="nu">{t('admin.users.name')}</label>
			<input id="nu" bind:value={nu.username} required minlength="2" maxlength="64" />
		</div>
		<div class="field">
			<label for="np">{t('admin.users.tempPassword')}</label>
			<input id="np" type="password" autocomplete="new-password" bind:value={nu.password} required minlength="10" />
		</div>
		<div class="field">
			<label for="nr">{t('admin.users.role')}</label>
			<select id="nr" bind:value={nu.role}>
				<option value="operator">{t('admin.users.roleOperatorDesc')}</option>
				<option value="viewer">{t('admin.users.roleViewerDesc')}</option>
				<option value="admin">{t('admin.users.roleAdminDesc')}</option>
			</select>
		</div>
	</div>
	<button class="primary" type="submit">{t('common.create')}</button>
</form>

<style>
	.actions {
		text-align: right;
		white-space: nowrap;
	}
	tr.disabled td {
		opacity: 0.6;
	}
	.new {
		max-width: 900px;
	}
</style>
