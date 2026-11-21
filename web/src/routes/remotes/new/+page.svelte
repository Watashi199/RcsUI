<script lang="ts">
	import { goto } from '$app/navigation';
	import RemoteForm from '#lib/components/RemoteForm.svelte';
	import { t } from '#lib/i18n/i18n.svelte.ts';
</script>

<svelte:head><title>{t('remotes.new.pageTitle')}</title></svelte:head>

<h1>{t('remotes.new.title')}</h1>
<RemoteForm ondone={() => goto('/remotes')} />
