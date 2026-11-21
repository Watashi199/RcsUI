<script lang="ts">
	import { get, put, post, errorMessage } from '#lib/api.ts';
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
			msg = 'Notifications enregistrées.';
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
			msg = 'Notification de test envoyée.';
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<h2>Notifications</h2>
{#if error}<div class="alert err">{error}</div>{/if}
{#if msg}<div class="alert ok">{msg}</div>{/if}
{#if cur}
	<form class="card box" onsubmit={save}>
		<div class="field">
			<label for="non">Notifier</label>
			<select id="non" bind:value={on}>
				<option value="never">jamais</option>
				<option value="failures">les problèmes (échec, partiel, interrompu, ignoré)</option>
				<option value="all">toutes les fins d'exécution</option>
			</select>
		</div>
		<label class="check"><input type="checkbox" bind:checked={includeDryRuns} /> Inclure les dry-runs</label>

		<div class="field">
			<label for="wh">Webhook (POST JSON)</label>
			<div class="row">
				<input
					id="wh"
					type="url"
					bind:value={webhookUrl}
					autocomplete="off"
					placeholder={cur.webhookSet && !clear.webhook ? `${cur.webhookUrl} (enregistré, laisser vide pour conserver)` : 'https://…'}
				/>
				{#if cur.webhookSet && !clear.webhook}
					<button type="button" onclick={() => ((clear.webhook = true), (webhookUrl = ''))}>Effacer</button>
				{/if}
			</div>
			<div class="hint">Corps : <span class="mono">{'{event, run, retryAt, url, title, message}'}</span>.</div>
		</div>

		<div class="field">
			<label for="nt">ntfy : URL du topic</label>
			<div class="row">
				<input
					id="nt"
					type="url"
					bind:value={ntfyUrl}
					autocomplete="off"
					placeholder={cur.ntfySet && !clear.ntfy ? `${cur.ntfyUrl} (enregistré, laisser vide pour conserver)` : 'https://ntfy.sh/mon-topic'}
				/>
				{#if cur.ntfySet && !clear.ntfy}
					<button type="button" onclick={() => ((clear.ntfy = true), (ntfyUrl = ''))}>Effacer</button>
				{/if}
			</div>
		</div>
		<div class="field">
			<label for="ntk">ntfy : jeton d'accès (optionnel)</label>
			<div class="row">
				<input
					id="ntk"
					type="password"
					bind:value={ntfyToken}
					autocomplete="new-password"
					placeholder={cur.ntfyTokenSet && !clear.token ? '•••••• (enregistré)' : 'tk_…'}
				/>
				{#if cur.ntfyTokenSet && !clear.token}
					<button type="button" onclick={() => ((clear.token = true), (ntfyToken = ''))}>Effacer</button>
				{/if}
			</div>
		</div>
		<div class="hint">Les URL et le jeton sont chiffrés au repos et ne sont jamais réaffichés.</div>

		<div class="row" style="margin-top:1rem">
			<button class="primary" type="submit" disabled={busy}>Enregistrer</button>
			<button type="button" onclick={test} disabled={busy}>Envoyer un test</button>
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
