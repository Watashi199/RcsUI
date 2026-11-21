<script lang="ts">
	import { goto } from '$app/navigation';
	import { post, errorMessage } from '#lib/api.ts';
	import { session, loadSession } from '#lib/session.svelte.ts';

	let current = $state('');
	let next = $state('');
	let confirm = $state('');
	let error = $state('');
	let busy = $state(false);
	const forced = $derived(session.user?.mustChangePassword ?? false);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		if (next !== confirm) {
			error = 'Les deux mots de passe ne correspondent pas.';
			return;
		}
		busy = true;
		try {
			await post('/auth/password', { current, new: next });
			await loadSession();
			await goto('/', { replace: true });
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Mot de passe · RcsUI</title></svelte:head>

<form class="card pw" onsubmit={submit}>
	<h1>Changer le mot de passe</h1>
	{#if forced}<div class="alert warn">Vous devez choisir un nouveau mot de passe avant de continuer.</div>{/if}
	{#if error}<div class="alert err" role="alert">{error}</div>{/if}
	<div class="field">
		<label for="c">Mot de passe actuel</label>
		<input id="c" type="password" autocomplete="current-password" bind:value={current} required />
	</div>
	<div class="field">
		<label for="n">Nouveau mot de passe</label>
		<input id="n" type="password" autocomplete="new-password" minlength="10" bind:value={next} required />
		<div class="hint">10 caractères minimum.</div>
	</div>
	<div class="field">
		<label for="k">Confirmation</label>
		<input id="k" type="password" autocomplete="new-password" bind:value={confirm} required />
	</div>
	<div class="row">
		<button class="primary" type="submit" disabled={busy}>Enregistrer</button>
		{#if !forced}<a href="/">Annuler</a>{/if}
	</div>
</form>

<style>
	.pw {
		width: min(420px, 100%);
		margin: 0 auto;
	}
</style>
