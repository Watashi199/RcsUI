<script lang="ts">
	// Export / import de la configuration : fichier chiffré par phrase de passe.
	import { post, errorMessage } from '#lib/api.ts';
	import { t } from '#lib/i18n/i18n.svelte.ts';

	type ImportResult = { jobs: number; remotes: number; sshIdentities: number; knownHosts: number; warnings: string[] };

	let exPass = $state('');
	let exConfirm = $state('');
	let imPass = $state('');
	let imFile = $state<FileList | null>(null);
	let error = $state('');
	let msg = $state('');
	let result = $state<ImportResult | null>(null);
	let busy = $state(false);

	async function doExport(e: SubmitEvent) {
		e.preventDefault();
		error = msg = '';
		if (exPass !== exConfirm) {
			error = t('admin.password.mismatch');
			return;
		}
		busy = true;
		try {
			const file = await post<unknown>('/config/export', { passphrase: exPass });
			const blob = new Blob([JSON.stringify(file, null, 2)], { type: 'application/json' });
			const a = document.createElement('a');
			a.href = URL.createObjectURL(blob);
			a.download = `rcsui-config-${new Date().toISOString().slice(0, 10)}.json`;
			a.click();
			URL.revokeObjectURL(a.href);
			exPass = exConfirm = '';
			msg = t('admin.config.exported');
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	async function doImport(e: SubmitEvent) {
		e.preventDefault();
		error = msg = '';
		result = null;
		const f = imFile?.[0];
		if (!f) return;
		busy = true;
		try {
			let file: unknown;
			try {
				file = JSON.parse(await f.text());
			} catch {
				throw new Error(t('admin.config.badFile'));
			}
			result = await post<ImportResult>('/config/import', { passphrase: imPass, file });
			imPass = '';
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<h2>{t('admin.config.title')}</h2>
<p class="small muted">{t('admin.config.help')}</p>
{#if error}<div class="alert err" role="alert">{error}</div>{/if}
{#if msg}<div class="alert ok">{msg}</div>{/if}
{#if result}
	<div class="alert ok">
		{t('admin.config.imported', { jobs: result.jobs, remotes: result.remotes, ssh: result.sshIdentities })}
		{#if result.warnings.length}
			<ul class="small">
				{#each result.warnings as w (w)}<li>{w}</li>{/each}
			</ul>
		{/if}
	</div>
{/if}

<div class="grid2 transfer">
	<form class="card" onsubmit={doExport}>
		<h3>{t('admin.config.export')}</h3>
		<div class="field">
			<label for="exp">{t('admin.config.passphrase')}</label>
			<input id="exp" type="password" autocomplete="new-password" minlength="12" bind:value={exPass} required />
			<div class="hint">{t('admin.config.passphraseHint')}</div>
		</div>
		<div class="field">
			<label for="exc">{t('admin.password.confirm')}</label>
			<input id="exc" type="password" autocomplete="new-password" bind:value={exConfirm} required />
		</div>
		<button class="primary" type="submit" disabled={busy}>{t('admin.config.download')}</button>
	</form>

	<form class="card" onsubmit={doImport}>
		<h3>{t('admin.config.import')}</h3>
		<p class="small muted">{t('admin.config.importHelp')}</p>
		<div class="field">
			<label for="imf">{t('admin.config.file')}</label>
			<input id="imf" type="file" accept="application/json,.json" bind:files={imFile} required />
		</div>
		<div class="field">
			<label for="imp">{t('admin.config.passphrase')}</label>
			<input id="imp" type="password" autocomplete="off" bind:value={imPass} required />
		</div>
		<button type="submit" disabled={busy}>{t('admin.config.importButton')}</button>
	</form>
</div>

<style>
	.transfer {
		max-width: 900px;
		align-items: start;
	}
	h3 {
		margin-top: 0;
		font-size: 1rem;
	}
</style>
