<script lang="ts">
	import { i18n, setLocale, t, LOCALES, LOCALE_NAMES, type Locale } from '#lib/i18n/i18n.svelte.ts';
</script>

<select
	class="locale"
	aria-label={t('common.language')}
	value={i18n.locale}
	onchange={(e) => setLocale(e.currentTarget.value as Locale)}
>
	{#each LOCALES as l (l)}<option value={l}>{LOCALE_NAMES[l]}</option>{/each}
</select>

<style>
	.locale {
		width: auto;
		padding: 0.15rem 0.3rem;
		font-size: 0.85rem;
	}
</style>
