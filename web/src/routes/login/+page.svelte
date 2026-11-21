<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { get, errorMessage } from '#lib/api.ts';
	import { login, loadSession, session } from '#lib/session.svelte.ts';
	import { t, type MessageKey } from '#lib/i18n/i18n.svelte.ts';

	const SSO_ERRORS: Record<string, MessageKey> = {
		sso_failed: 'admin.login.ssoFailed',
		sso_not_provisioned: 'admin.login.ssoNotProvisioned',
		sso_disabled: 'admin.login.ssoDisabled',
		sso_last_admin: 'admin.login.ssoLastAdmin'
	};

	let username = $state('');
	let password = $state('');
	const ssoError: MessageKey | undefined = SSO_ERRORS[page.url.searchParams.get('error') ?? ''];
	let error = $state(ssoError ? t(ssoError) : '');
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

<svelte:head><title>{t('admin.login.title')} · RcsUI</title></svelte:head>

<form class="card login" onsubmit={submit}>
	<h1>RcsUI</h1>
	{#if error}<div class="alert err" role="alert">{error}</div>{/if}
	<div class="field">
		<label for="u">{t('admin.login.username')}</label>
		<input id="u" autocomplete="username" bind:value={username} required />
	</div>
	<div class="field">
		<label for="p">{t('admin.login.password')}</label>
		<input id="p" type="password" autocomplete="current-password" bind:value={password} required />
	</div>
	<button class="primary" type="submit" disabled={busy}>{busy ? t('admin.login.signingIn') : t('admin.login.signIn')}</button>
	{#if oidc}
		<div class="sep muted small">{t('admin.login.or')}</div>
		<a class="btn sso" href={ssoHref} data-sveltekit-reload>{t('admin.login.signInSso')}</a>
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
