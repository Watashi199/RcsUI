<script lang="ts">
	import { goto } from '$app/navigation';
	import { post } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import type { Job, JobInput } from '#lib/types.ts';
	import JobForm from '#lib/components/JobForm.svelte';

	const initial: JobInput = {
		name: '',
		engine: session.info?.rsyncVersion ? 'rsync' : 'rclone',
		mode: 'copy',
		source: { type: 'local', path: '', contentsOnly: true },
		destination: { type: 'local', path: '' },
		options: { v: 1 },
		schedule: null,
		retryInterrupted: false,
		retryMax: 0,
		retryDelaySec: 300,
		catchUp: false
	};

	async function create(j: JobInput) {
		const created = await post<Job>('/jobs', j);
		await goto('/jobs/' + created.id);
	}
</script>

<svelte:head><title>Nouveau job · RcsUI</title></svelte:head>

<h1>Nouveau job</h1>
{#if !session.info?.rsyncVersion && !session.info?.rcloneEnabled}
	<div class="alert err">Aucun moteur disponible sur ce serveur.</div>
{:else}
	<JobForm {initial} submitLabel="Créer le job" onsubmit={create} />
{/if}
