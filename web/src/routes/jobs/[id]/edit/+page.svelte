<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { get, put, errorMessage } from '#lib/api.ts';
	import type { Job, JobInput } from '#lib/types.ts';
	import JobForm from '#lib/components/JobForm.svelte';
	import { t } from '#lib/i18n/i18n.svelte.ts';

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
					catchUp: j.catchUp ?? false,
					exclusiveDestination: j.exclusiveDestination ?? false
				};
			})
			.catch((e) => (error = errorMessage(e)));
	});

	async function save(j: JobInput) {
		await put('/jobs/' + id, j);
		await goto('/jobs/' + id);
	}
</script>

<svelte:head><title>{t('jobs.edit.title')} · RcsUI</title></svelte:head>

<h1>{t('jobs.edit.title')}</h1>
{#if error}<div class="alert err">{error}</div>{/if}
{#if initial}
	<JobForm {initial} submitLabel={t('common.save')} onsubmit={save} />
{/if}
