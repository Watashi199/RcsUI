<script lang="ts">
	import { get, put, post, errorMessage } from '#lib/api.ts';
	import { t } from '#lib/i18n/i18n.svelte.ts';
	import type { NotificationInput, NotificationSettings, NotifyMode } from '#lib/types.ts';

	// Les URL et le jeton ne sont jamais renvoyés par l'API : un champ laissé
	// vide conserve la valeur enregistrée ; « Effacer » la supprime.
	let cur = $state<NotificationSettings | null>(null);
	let on = $state<NotifyMode>('never');
	let includeDryRuns = $state(false);
	let webhookUrl = $state('');
	let ntfyUrl = $state('');
	let ntfyToken = $state('');
	let clear = $state({ webhook: false, ntfy: false, token: false });
	let error = $state('');
	let msg = $state('');
	let busy = $state(false);

	function load(s: NotificationSettings) {
		cur = s;
		on = s.on;
		includeDryRuns = s.includeDryRuns;
		webhookUrl = ntfyUrl = ntfyToken = '';
		clear = { webhook: false, ntfy: false, token: false };
	}

	(async () => {
		try {
			load(await get<NotificationSettings>('/settings/notifications'));
		} catch (e) {
			error = errorMessage(e);
		}
	})();

	function input(): NotificationInput {
		const i: NotificationInput = { on, includeDryRuns };
		if (webhookUrl.trim()) i.webhookUrl = webhookUrl.trim();
		else if (clear.webhook) i.webhookUrl = '';
		if (ntfyUrl.trim()) i.ntfyUrl = ntfyUrl.trim();
		else if (clear.ntfy) i.ntfyUrl = '';
		if (ntfyToken) i.ntfyToken = ntfyToken;
		else if (clear.token) i.ntfyToken = '';
		return i;
	}

	async function save(e: SubmitEvent) {
		e.preventDefault();
		error = msg = '';
		busy = true;
		try {
			load(await put<NotificationSettings>('/settings/notifications', input()));
			msg = t('admin.notify.saved');
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	async function test() {
		error = msg = '';
		busy = true;
		try {
			await post('/settings/notifications/test', input());
			msg = t('admin.notify.testSent');
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<h2>{t('admin.notify.title')}</h2>
{#if error}<div class="alert err">{error}</div>{/if}
{#if msg}<div class="alert ok">{msg}</div>{/if}
{#if cur}
	<form class="card box" onsubmit={save}>
		<div class="field">
			<label for="non">{t('admin.notify.on')}</label>
			<select id="non" bind:value={on}>
				<option value="never">{t('admin.notify.never')}</option>
				<option value="failures">{t('admin.notify.failures')}</option>
				<option value="all">{t('admin.notify.all')}</option>
			</select>
		</div>
		<label class="check"><input type="checkbox" bind:checked={includeDryRuns} /> {t('admin.notify.includeDryRuns')}</label>

		<div class="field">
			<label for="wh">{t('admin.notify.webhook')}</label>
			<div class="row">
				<input
					id="wh"
					type="url"
					bind:value={webhookUrl}
					autocomplete="off"
					placeholder={cur.webhookSet && !clear.webhook ? t('admin.notify.savedKeepPlaceholder', { url: cur.webhookUrl }) : 'https://…'}
				/>
				{#if cur.webhookSet && !clear.webhook}
					<button type="button" onclick={() => ((clear.webhook = true), (webhookUrl = ''))}>{t('admin.notify.clear')}</button>
				{/if}
			</div>
			<div class="hint">{t('admin.notify.body')} <span class="mono">{'{event, run, retryAt, url, title, message}'}</span>.</div>
		</div>

		<div class="field">
			<label for="nt">{t('admin.notify.ntfyUrl')}</label>
			<div class="row">
				<input
					id="nt"
					type="url"
					bind:value={ntfyUrl}
					autocomplete="off"
					placeholder={cur.ntfySet && !clear.ntfy ? t('admin.notify.savedKeepPlaceholder', { url: cur.ntfyUrl }) : t('admin.notify.ntfyUrlPlaceholder')}
				/>
				{#if cur.ntfySet && !clear.ntfy}
					<button type="button" onclick={() => ((clear.ntfy = true), (ntfyUrl = ''))}>{t('admin.notify.clear')}</button>
				{/if}
			</div>
		</div>
		<div class="field">
			<label for="ntk">{t('admin.notify.ntfyToken')}</label>
			<div class="row">
				<input
					id="ntk"
					type="password"
					bind:value={ntfyToken}
					autocomplete="new-password"
					placeholder={cur.ntfyTokenSet && !clear.token ? t('admin.notify.tokenSaved') : 'tk_…'}
				/>
				{#if cur.ntfyTokenSet && !clear.token}
					<button type="button" onclick={() => ((clear.token = true), (ntfyToken = ''))}>{t('admin.notify.clear')}</button>
				{/if}
			</div>
		</div>
		<div class="hint">{t('admin.notify.encryptedHint')}</div>

		<div class="row" style="margin-top:1rem">
			<button class="primary" type="submit" disabled={busy}>{t('common.save')}</button>
			<button type="button" onclick={test} disabled={busy}>{t('admin.notify.sendTest')}</button>
		</div>
	</form>
{/if}

<style>
	.box {
		max-width: 700px;
	}
	.row input {
		flex: 1;
		min-width: 0;
	}
</style>
