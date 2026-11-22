<script lang="ts">
	import { get, put, errorMessage } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import { t } from '#lib/i18n/i18n.svelte.ts';
	import type { Settings, SystemInfo } from '#lib/types.ts';
	import NotificationsForm from '#lib/components/NotificationsForm.svelte';

	let s = $state<Settings | null>(null);
	let info = $state<SystemInfo | null>(null);
	let error = $state('');
	let msg = $state('');

	(async () => {
		try {
			[s, info] = await Promise.all([get<Settings>('/settings'), get<SystemInfo>('/system/info')]);
		} catch (e) {
			error = errorMessage(e);
		}
	})();

	async function save(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		msg = '';
		try {
			s = await put<Settings>('/settings', s);
			session.info = await get<SystemInfo>('/system/info');
			msg = t('admin.settings.saved');
		} catch (err) {
			error = errorMessage(err);
		}
	}
</script>

<svelte:head><title>{t('common.nav.settings')} · RcsUI</title></svelte:head>

<h1>{t('common.nav.settings')}</h1>
{#if error}<div class="alert err">{error}</div>{/if}
{#if msg}<div class="alert ok">{msg}</div>{/if}

{#if s}
	<form class="card box" onsubmit={save}>
		<div class="field">
			<label for="mc">{t('admin.settings.maxConcurrent')}</label>
			<input id="mc" type="number" min="1" max="64" bind:value={s.maxConcurrent} required />
			<div class="hint">{t('admin.settings.maxConcurrentHint')}</div>
		</div>
		<div class="field">
			<label for="lr">{t('admin.settings.logRetention')}</label>
			<input id="lr" type="number" min="1" max="3650" bind:value={s.logRetentionDays} required />
		</div>
		<label class="check">
			<input type="checkbox" bind:checked={s.rsyncVanishedIsSuccess} />
			{t('admin.settings.vanishedIsSuccess')}
		</label>
		<div style="margin-top:1rem"><button class="primary" type="submit">{t('common.save')}</button></div>
	</form>
{/if}

{#if s}
	<NotificationsForm />
{/if}

{#if info}
	<h2>{t('admin.settings.system')}</h2>
	<div class="card box">
		<dl>
			<dt>{t('admin.settings.version')}</dt>
			<dd>{info.version}</dd>
			<dt>rsync</dt>
			<dd>{info.rsyncVersion || t('admin.settings.unavailable')}</dd>
			<dt>rclone</dt>
			<dd>{info.rcloneEnabled ? info.rcloneVersion : t('admin.settings.disabled')}</dd>
			<dt>{t('admin.settings.allowedRoots')}</dt>
			<dd class="mono">{info.allowedRoots.length ? info.allowedRoots.join(', ') : t('admin.settings.noRoots')}</dd>
		</dl>
	</div>
{/if}

<style>
	.box {
		max-width: 700px;
	}
	dl {
		display: grid;
		grid-template-columns: 220px 1fr;
		gap: 0.4rem 1rem;
		margin: 0;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
	}
</style>
