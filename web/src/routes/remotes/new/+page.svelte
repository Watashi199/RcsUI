<script lang="ts">
	import { goto } from '$app/navigation';
	import RemoteForm from '#lib/components/RemoteForm.svelte';
</script>

<svelte:head><title>Nouveau remote · RcsUI</title></svelte:head>

<h1>Nouveau remote</h1>
<RemoteForm ondone={() => goto('/remotes')} />
