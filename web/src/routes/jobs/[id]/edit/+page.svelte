<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { get, put, errorMessage } from '#lib/api.ts';
	import type { Job, JobInput } from '#lib/types.ts';
	import JobForm from '#lib/components/JobForm.svelte';

	const id = $derived(page.params.id);
	let initial = $state<JobInput | null>(null);
	let error = $state('');

	$effect(() => {
		get<Job>('/jobs/' + id)
			.then((j) => {
				initial = {
					name: j.name,
					engine: j.engine,
					mode: j.mode,
					source: j.source,
					destination: j.destination,
					options: j.options ?? { v: 1 },
					schedule: j.scheduleCron ? { cron: j.scheduleCron, enabled: j.scheduleEnabled } : null,
					retryInterrupted: j.retryInterrupted,
					retryMax: j.retryMax ?? 0,
					retryDelaySec: j.retryDelaySec || 300,
					catchUp: j.catchUp ?? false
				};
			})
			.catch((e) => (error = errorMessage(e)));
	});

	async function save(j: JobInput) {
		await put('/jobs/' + id, j);
		await goto('/jobs/' + id);
	}
</script>

<svelte:head><title>Modifier le job · RcsUI</title></svelte:head>

<h1>Modifier le job</h1>
{#if error}<div class="alert err">{error}</div>{/if}
{#if initial}
	<JobForm {initial} submitLabel="Enregistrer" onsubmit={save} />
{/if}
