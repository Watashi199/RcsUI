<script lang="ts">
	import { get, post, patch, del, errorMessage } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import { relative } from '#lib/format.ts';
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
		}, 'Utilisateur créé (il devra changer son mot de passe).');
	}
	const setRole = (u: User, role: Role) => act(() => patch('/users/' + u.id, { role }), 'Rôle modifié.');
	const toggle = (u: User) =>
		act(() => patch('/users/' + u.id, { disabled: !u.disabled }), u.disabled ? 'Compte réactivé.' : 'Compte désactivé.');
	function reset(u: User) {
		const pw = prompt(`Nouveau mot de passe temporaire pour ${u.username} (10 caractères min.) :`);
		if (pw) void act(() => patch('/users/' + u.id, { password: pw }), 'Mot de passe réinitialisé.');
	}
	function remove(u: User) {
		if (confirm(`Supprimer ${u.username} ?`)) void act(() => del('/users/' + u.id), 'Utilisateur supprimé.');
	}
</script>

<svelte:head><title>Utilisateurs · RcsUI</title></svelte:head>

<h1>Utilisateurs</h1>
{#if error}<div class="alert err">{error}</div>{/if}
{#if msg}<div class="alert ok">{msg}</div>{/if}

<div class="table-wrap">
	<table>
		<thead><tr><th>Utilisateur</th><th>Rôle</th><th class="hide-sm">Dernière connexion</th><th></th></tr></thead>
		<tbody>
			{#each users as u (u.id)}
				<tr class:disabled={u.disabled}>
					<td>
						{u.username}
						{#if u.disabled}<span class="muted small">(désactivé)</span>{/if}
						{#if u.mustChangePassword}<span class="muted small">(mot de passe à changer)</span>{/if}
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
							<button onclick={() => reset(u)}>Réinitialiser</button>
							<button onclick={() => toggle(u)}>{u.disabled ? 'Réactiver' : 'Désactiver'}</button>
							<button class="danger" onclick={() => remove(u)}>Supprimer</button>
						{/if}
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>

<h2>Nouvel utilisateur</h2>
<form class="card new" onsubmit={create}>
	<div class="grid2">
		<div class="field">
			<label for="nu">Nom</label>
			<input id="nu" bind:value={nu.username} required minlength="2" maxlength="64" />
		</div>
		<div class="field">
			<label for="np">Mot de passe temporaire</label>
			<input id="np" type="password" autocomplete="new-password" bind:value={nu.password} required minlength="10" />
		</div>
		<div class="field">
			<label for="nr">Rôle</label>
			<select id="nr" bind:value={nu.role}>
				<option value="operator">operator — crée et lance ses jobs</option>
				<option value="viewer">viewer — lecture seule des jobs accordés</option>
				<option value="admin">admin — tous les droits</option>
			</select>
		</div>
	</div>
	<button class="primary" type="submit">Créer</button>
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
