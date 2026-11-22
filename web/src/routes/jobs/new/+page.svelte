<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { get, post, errorMessage } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import type { Job, JobInput } from '#lib/types.ts';
	import JobForm from '#lib/components/JobForm.svelte';
	import { t } from '#lib/i18n/i18n.svelte.ts';
	import { duplicateInput } from '#lib/jobs.ts';

	const blank: JobInput = {
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
		catchUp: false,
		exclusiveDestination: false
	};

	// ?from=<id> : duplication d'un job existant.
	const from = page.url.searchParams.get('from');
	let initial = $state<JobInput | null>(from ? null : blank);
	let sourceName = $state('');
	let error = $state('');
	if (from) {
		get<Job>('/jobs/' + encodeURIComponent(from))
			.then((j) => {
				sourceName = j.name;
				initial = duplicateInput(j, (name) => t('jobs.duplicate.name', { name }));
			})
			.catch((e) => (error = errorMessage(e)));
	}

	async function create(j: JobInput) {
		const created = await post<Job>('/jobs', j);
		await goto('/jobs/' + created.id);
	}
</script>

<svelte:head><title>{t('jobs.new.title')} · RcsUI</title></svelte:head>

<h1>{sourceName ? t('jobs.duplicate.title', { name: sourceName }) : t('jobs.new.title')}</h1>
{#if error}<div class="alert err">{error}</div>{/if}
{#if !session.info?.rsyncVersion && !session.info?.rcloneEnabled}
	<div class="alert err">{t('jobs.new.noEngine')}</div>
{:else if initial}
	{#if sourceName && initial.schedule}<p class="small muted">{t('jobs.duplicate.scheduleOff')}</p>{/if}
	<JobForm {initial} submitLabel={t('jobs.new.submit')} onsubmit={create} />
{/if}
