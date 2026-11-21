<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { get, put, errorMessage } from '#lib/api.ts';
	import type { User } from '#lib/types.ts';
	import RemoteForm from '#lib/components/RemoteForm.svelte';

	const name = $derived(page.params.name ?? '');
	let operators = $state<User[]>([]);
	let granted = $state<Record<number, boolean>>({});
	let msg = $state('');
	let error = $state('');

	$effect(() => {
		const n = name;
		(async () => {
			try {
				const [u, g] = await Promise.all([
					get<{ items: User[] }>('/users'),
					get<{ userIds: number[] }>(`/remotes/${encodeURIComponent(n)}/grants`)
				]);
				operators = u.items.filter((x) => x.role === 'operator');
				granted = Object.fromEntries(g.userIds.map((id) => [id, true]));
			} catch (e) {
				error = errorMessage(e);
			}
		})();
	});

	async function saveGrants() {
		msg = '';
		try {
			const ids = Object.entries(granted)
				.filter(([, v]) => v)
				.map(([k]) => Number(k));
			await put(`/remotes/${encodeURIComponent(name)}/grants`, { userIds: ids });
			msg = 'Accès enregistrés.';
		} catch (e) {
			error = errorMessage(e);
		}
	}
</script>

<svelte:head><title>{name} · Remotes · RcsUI</title></svelte:head>

<h1>Remote « {name} »</h1>
{#key name}
	<RemoteForm {name} ondone={() => goto('/remotes')} />
{/key}

<h2>Opérateurs autorisés</h2>
{#if error}<div class="alert err">{error}</div>{/if}
{#if msg}<div class="alert ok">{msg}</div>{/if}
<div class="card" style="max-width:900px">
	{#if operators.length === 0}
		<span class="muted">Aucun utilisateur de rôle « operator ».</span>
	{:else}
		{#each operators as u (u.id)}
			<label class="check"><input type="checkbox" bind:checked={granted[u.id]} /> {u.username}</label>
		{/each}
		<button style="margin-top:0.6rem" onclick={saveGrants}>Enregistrer les accès</button>
	{/if}
	<div class="muted small">Les administrateurs ont accès à tous les remotes ; les lecteurs à aucun.</div>
</div>
