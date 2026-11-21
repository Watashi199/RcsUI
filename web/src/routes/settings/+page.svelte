<script lang="ts">
	import { get, put, errorMessage } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
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
			msg = 'Réglages enregistrés.';
		} catch (err) {
			error = errorMessage(err);
		}
	}
</script>

<svelte:head><title>Réglages · RcsUI</title></svelte:head>

<h1>Réglages</h1>
{#if error}<div class="alert err">{error}</div>{/if}
{#if msg}<div class="alert ok">{msg}</div>{/if}

{#if s}
	<form class="card box" onsubmit={save}>
		<div class="field">
			<label for="mc">Exécutions simultanées maximum</label>
			<input id="mc" type="number" min="1" max="64" bind:value={s.maxConcurrent} required />
			<div class="hint">Les exécutions au-delà attendent en file (ordre d'arrivée). Appliqué immédiatement.</div>
		</div>
		<div class="field">
			<label for="lr">Rétention de l'historique et des logs (jours)</label>
			<input id="lr" type="number" min="1" max="3650" bind:value={s.logRetentionDays} required />
		</div>
		<label class="check">
			<input type="checkbox" bind:checked={s.rsyncVanishedIsSuccess} />
			rsync : considérer « fichiers disparus pendant le transfert » (code 24) comme un succès
		</label>
		<div style="margin-top:1rem"><button class="primary" type="submit">Enregistrer</button></div>
	</form>
{/if}

{#if s}
	<NotificationsForm />
{/if}

{#if info}
	<h2>Système</h2>
	<div class="card box">
		<dl>
			<dt>Version</dt>
			<dd>{info.version}</dd>
			<dt>rsync</dt>
			<dd>{info.rsyncVersion || 'indisponible'}</dd>
			<dt>rclone</dt>
			<dd>{info.rcloneEnabled ? info.rcloneVersion : 'désactivé'}</dd>
			<dt>Racines locales autorisées</dt>
			<dd class="mono">{info.allowedRoots.length ? info.allowedRoots.join(', ') : 'aucune'}</dd>
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
