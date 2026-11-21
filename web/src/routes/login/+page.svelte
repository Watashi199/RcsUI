<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { get, errorMessage } from '#lib/api.ts';
	import { login, loadSession, session } from '#lib/session.svelte.ts';

	const SSO_ERRORS: Record<string, string> = {
		sso_failed: 'La connexion SSO a échoué.',
		sso_not_provisioned: "Aucun compte n'est associé à cette identité SSO. Contactez un administrateur.",
		sso_disabled: 'Ce compte est désactivé.',
		sso_last_admin: "Connexion refusée : elle retirerait le dernier administrateur (vérifiez les groupes de l'IdP)."
	};

	let username = $state('');
	let password = $state('');
	let error = $state(SSO_ERRORS[page.url.searchParams.get('error') ?? ''] ?? '');
	let busy = $state(false);
	let oidc = $state(false);

	get<{ oidcEnabled: boolean }>('/auth/config')
		.then((c) => (oidc = c.oidcEnabled))
		.catch(() => {});

	const ssoHref = $derived(
		'/api/v1/auth/oidc/login?next=' + encodeURIComponent(page.url.searchParams.get('next') ?? '/')
	);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			await login(username, password);
			await loadSession();
			const next = page.url.searchParams.get('next');
			// Chemin interne uniquement : pas de redirection ouverte.
			const target = next && next.startsWith('/') && !next.startsWith('//') ? next : '/';
			await goto(session.user?.mustChangePassword ? '/password' : target, { replace: true });
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Connexion · RcsUI</title></svelte:head>

<form class="card login" onsubmit={submit}>
	<h1>RcsUI</h1>
	{#if error}<div class="alert err" role="alert">{error}</div>{/if}
	<div class="field">
		<label for="u">Utilisateur</label>
		<input id="u" autocomplete="username" bind:value={username} required />
	</div>
	<div class="field">
		<label for="p">Mot de passe</label>
		<input id="p" type="password" autocomplete="current-password" bind:value={password} required />
	</div>
	<button class="primary" type="submit" disabled={busy}>{busy ? 'Connexion…' : 'Se connecter'}</button>
	{#if oidc}
		<div class="sep muted small">ou</div>
		<a class="btn sso" href={ssoHref} data-sveltekit-reload>Se connecter avec SSO</a>
	{/if}
</form>

<style>
	.login {
		width: min(360px, 100%);
	}
	.login button,
	.sso {
		width: 100%;
		justify-content: center;
	}
	.sep {
		text-align: center;
		margin: 0.7rem 0;
	}
</style>
