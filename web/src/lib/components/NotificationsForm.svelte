<script lang="ts">
	import { get, put, post, errorMessage } from '#lib/api.ts';
	import { t } from '#lib/i18n/i18n.svelte.ts';
	import type { NotificationInput, NotificationSettings, NotifyMode, SmtpSecurity } from '#lib/types.ts';

	// Les URL et le jeton ne sont jamais renvoyés par l'API : un champ laissé
	// vide conserve la valeur enregistrée ; « Effacer » la supprime.
	let cur = $state<NotificationSettings | null>(null);
	let on = $state<NotifyMode>('never');
	let includeDryRuns = $state(false);
	let webhookUrl = $state('');
	let ntfyUrl = $state('');
	let ntfyToken = $state('');
	let gotifyUrl = $state('');
	let gotifyToken = $state('');
	let clear = $state({ webhook: false, ntfy: false, token: false, gotify: false, gotifyToken: false });
	// E-mail : champs non secrets réaffichés ; mot de passe vide = conservé.
	const noSmtp = { host: '', port: 587, security: 'starttls' as SmtpSecurity, username: '', password: '', from: '', to: '' };
	let smtpOn = $state(false);
	let smtp = $state({ ...noSmtp });
	let error = $state('');
	let msg = $state('');
	let busy = $state(false);

	function load(s: NotificationSettings) {
		cur = s;
		on = s.on;
		includeDryRuns = s.includeDryRuns;
		webhookUrl = ntfyUrl = ntfyToken = gotifyUrl = gotifyToken = '';
		clear = { webhook: false, ntfy: false, token: false, gotify: false, gotifyToken: false };
		smtpOn = !!s.smtp;
		smtp = s.smtp ? { ...s.smtp, password: '', to: s.smtp.to.join(', ') } : { ...noSmtp };
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
		if (gotifyUrl.trim()) i.gotifyUrl = gotifyUrl.trim();
		else if (clear.gotify) i.gotifyUrl = '';
		if (gotifyToken) i.gotifyToken = gotifyToken;
		else if (clear.gotifyToken) i.gotifyToken = '';
		if (smtpOn) {
			i.smtp = {
				host: smtp.host.trim(),
				port: Number(smtp.port),
				security: smtp.security,
				username: smtp.username.trim(),
				from: smtp.from.trim(),
				to: smtp.to
					.split(/[,\s]+/)
					.map((a) => a.trim())
					.filter(Boolean)
			};
			if (smtp.password) i.smtp.password = smtp.password;
		} else if (cur?.smtp) i.smtp = { host: '' };
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
		<div class="field">
			<label for="gu">{t('admin.notify.gotifyUrl')}</label>
			<div class="row">
				<input
					id="gu"
					type="url"
					bind:value={gotifyUrl}
					autocomplete="off"
					placeholder={cur.gotifySet && !clear.gotify ? t('admin.notify.savedKeepPlaceholder', { url: cur.gotifyUrl }) : 'https://gotify.example'}
				/>
				{#if cur.gotifySet && !clear.gotify}
					<button type="button" onclick={() => ((clear.gotify = true), (gotifyUrl = ''))}>{t('admin.notify.clear')}</button>
				{/if}
			</div>
		</div>
		<div class="field">
			<label for="gt">{t('admin.notify.gotifyToken')}</label>
			<div class="row">
				<input
					id="gt"
					type="password"
					bind:value={gotifyToken}
					autocomplete="new-password"
					placeholder={cur.gotifyTokenSet && !clear.gotifyToken ? t('admin.notify.tokenSaved') : ''}
				/>
				{#if cur.gotifyTokenSet && !clear.gotifyToken}
					<button type="button" onclick={() => ((clear.gotifyToken = true), (gotifyToken = ''))}>{t('admin.notify.clear')}</button>
				{/if}
			</div>
		</div>

		<label class="check"><input type="checkbox" bind:checked={smtpOn} /> {t('admin.notify.email')}</label>
		{#if smtpOn}
			<div class="grid2">
				<div class="field">
					<label for="sh">{t('admin.notify.smtpHost')}</label>
					<input id="sh" bind:value={smtp.host} required placeholder="smtp.example.org" />
				</div>
				<div class="field">
					<label for="sp">{t('admin.notify.smtpPort')}</label>
					<input id="sp" type="number" min="1" max="65535" bind:value={smtp.port} required />
				</div>
				<div class="field">
					<label for="ss">{t('admin.notify.smtpSecurity')}</label>
					<select id="ss" bind:value={smtp.security}>
						<option value="starttls">{t('admin.notify.smtpStartTLS')}</option>
						<option value="tls">{t('admin.notify.smtpTLS')}</option>
						<option value="none">{t('admin.notify.smtpNone')}</option>
					</select>
				</div>
				<div class="field">
					<label for="su">{t('admin.notify.smtpUser')}</label>
					<input id="su" bind:value={smtp.username} autocomplete="off" />
				</div>
				<div class="field">
					<label for="spw">{t('admin.notify.smtpPassword')}</label>
					<input
						id="spw"
						type="password"
						bind:value={smtp.password}
						autocomplete="new-password"
						placeholder={cur.smtp?.passwordSet ? t('admin.notify.tokenSaved') : ''}
					/>
				</div>
				<div class="field">
					<label for="sf">{t('admin.notify.smtpFrom')}</label>
					<input id="sf" type="email" bind:value={smtp.from} required placeholder="rcsui@example.org" />
				</div>
			</div>
			<div class="field">
				<label for="st">{t('admin.notify.smtpTo')}</label>
				<input id="st" bind:value={smtp.to} required placeholder="ops@example.org" />
			</div>
		{/if}
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
