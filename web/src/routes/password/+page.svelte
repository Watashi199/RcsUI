<script lang="ts">
	import { goto } from '$app/navigation';
	import { post, errorMessage } from '#lib/api.ts';
	import { session, loadSession } from '#lib/session.svelte.ts';
	import { t } from '#lib/i18n/i18n.svelte.ts';

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
			error = t('admin.password.mismatch');
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

<svelte:head><title>{t('common.nav.password')} · RcsUI</title></svelte:head>

<form class="card pw" onsubmit={submit}>
	<h1>{t('admin.password.title')}</h1>
	{#if forced}<div class="alert warn">{t('admin.password.forced')}</div>{/if}
	{#if error}<div class="alert err" role="alert">{error}</div>{/if}
	<div class="field">
		<label for="c">{t('admin.password.current')}</label>
		<input id="c" type="password" autocomplete="current-password" bind:value={current} required />
	</div>
	<div class="field">
		<label for="n">{t('admin.password.new')}</label>
		<input id="n" type="password" autocomplete="new-password" minlength="10" bind:value={next} required />
		<div class="hint">{t('admin.password.minLength')}</div>
	</div>
	<div class="field">
		<label for="k">{t('admin.password.confirm')}</label>
		<input id="k" type="password" autocomplete="new-password" bind:value={confirm} required />
	</div>
	<div class="row">
		<button class="primary" type="submit" disabled={busy}>{t('common.save')}</button>
		{#if !forced}<a href="/">{t('common.cancel')}</a>{/if}
	</div>
</form>

<style>
	.pw {
		width: min(420px, 100%);
		margin: 0 auto;
	}
</style>
