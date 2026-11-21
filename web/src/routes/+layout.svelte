<script lang="ts">
	import '#lib/app.css';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { setUnauthorizedHandler } from '#lib/api.ts';
	import { session, loadSession, logout, isAdmin } from '#lib/session.svelte.ts';
	import { live, startLive, stopLive, activeRuns } from '#lib/live.svelte.ts';

	let { children } = $props();
	let error = $state('');

	const PUBLIC = ['/login'];

	function toLogin() {
		stopLive();
		session.user = null;
		if (page.url.pathname !== '/login') {
			const next = page.url.pathname + page.url.search;
			void goto('/login?next=' + encodeURIComponent(next), { replace: true });
		}
	}

	onMount(async () => {
		setUnauthorizedHandler(toLogin);
		try {
			const ok = await loadSession();
			if (!ok && !PUBLIC.includes(page.url.pathname)) toLogin();
		} catch (e) {
			error = 'Serveur injoignable : ' + (e instanceof Error ? e.message : String(e));
		}
	});

	// Garde : changement de mot de passe obligatoire, flux SSE global une fois connecté.
	$effect(() => {
		const u = session.user;
		if (!session.loaded) return;
		if (u?.mustChangePassword && page.url.pathname !== '/password') {
			void goto('/password', { replace: true });
		} else if (u && !u.mustChangePassword) {
			startLive(toLogin);
		}
	});

	async function doLogout() {
		stopLive();
		await logout();
		await goto('/login', { replace: true });
	}

	const nav = $derived([
		{ href: '/', label: 'Tableau de bord' },
		{ href: '/jobs', label: 'Jobs' },
		{ href: '/runs', label: 'Exécutions' },
		...(session.info?.rcloneEnabled ? [{ href: '/remotes', label: 'Remotes' }] : []),
		...(session.info?.sshAvailable && isAdmin() ? [{ href: '/ssh', label: 'SSH' }] : []),
		...(isAdmin()
			? [
					{ href: '/users', label: 'Utilisateurs' },
					{ href: '/settings', label: 'Réglages' }
				]
			: [])
	]);

	function active(href: string) {
		const p = page.url.pathname;
		return href === '/' ? p === '/' : p === href || p.startsWith(href + '/');
	}
	const running = $derived(activeRuns().length);
</script>

{#if error}
	<div class="center"><div class="alert err">{error}</div></div>
{:else if !session.loaded}
	<div class="center muted">Chargement…</div>
{:else if !session.user || session.user.mustChangePassword}
	<main class="bare">{@render children()}</main>
{:else}
	<div class="shell">
		<aside>
			<div class="brand">RcsUI</div>
			<nav>
				{#each nav as n (n.href)}
					<a href={n.href} class:active={active(n.href)}>
						{n.label}
						{#if n.href === '/runs' && running > 0}<span class="pill">{running}</span>{/if}
					</a>
				{/each}
			</nav>
			<div class="foot">
				<div class="small">
					<span class="dot" class:on={live.connected} title={live.connected ? 'Temps réel connecté' : 'Temps réel déconnecté'}></span>
					{session.user.username} <span class="muted">· {session.user.role}</span>
				</div>
				<div class="row small">
					<a href="/password">Mot de passe</a>
					<button class="link" onclick={doLogout}>Déconnexion</button>
				</div>
				{#if session.info}<div class="muted small">v{session.info.version}</div>{/if}
			</div>
		</aside>
		<main>{@render children()}</main>
	</div>
{/if}

<style>
	.center {
		min-height: 100vh;
		display: grid;
		place-items: center;
		padding: 1rem;
	}
	.bare {
		min-height: 100vh;
		display: grid;
		place-items: center;
		padding: 1rem;
	}
	.shell {
		display: grid;
		grid-template-columns: 220px 1fr;
		min-height: 100vh;
	}
	aside {
		background: var(--surface);
		border-right: 1px solid var(--border);
		display: flex;
		flex-direction: column;
		padding: 1rem 0.75rem;
		position: sticky;
		top: 0;
		height: 100vh;
	}
	.brand {
		font-weight: 700;
		font-size: 1.15rem;
		padding: 0 0.5rem 1rem;
	}
	nav {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	nav a {
		color: var(--text);
		padding: 0.45rem 0.6rem;
		border-radius: 6px;
		display: flex;
		justify-content: space-between;
		align-items: center;
	}
	nav a:hover {
		background: var(--surface-2);
		text-decoration: none;
	}
	nav a.active {
		background: var(--info-bg);
		color: var(--accent);
		font-weight: 600;
	}
	.pill {
		background: var(--accent);
		color: var(--accent-text);
		border-radius: 999px;
		font-size: 0.75rem;
		padding: 0 0.45rem;
	}
	.foot {
		margin-top: auto;
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		padding: 0 0.5rem;
	}
	.dot {
		display: inline-block;
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--muted);
		margin-right: 0.3rem;
	}
	.dot.on {
		background: var(--ok);
	}
	button.link {
		border: none;
		background: none;
		color: var(--accent);
		padding: 0;
	}
	main {
		padding: 1.5rem;
		min-width: 0;
	}
	@media (max-width: 720px) {
		.shell {
			grid-template-columns: 1fr;
		}
		aside {
			position: static;
			height: auto;
		}
		nav {
			flex-direction: row;
			flex-wrap: wrap;
		}
		main {
			padding: 1rem;
		}
	}
</style>
