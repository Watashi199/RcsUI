<script lang="ts">
	// Historique d'un job : durée et volume des derniers runs terminés, en deux
	// graphiques à un seul axe (jamais de double axe). Une série par graphique :
	// le titre la nomme, pas de légende. Valeurs au survol et au clavier ; le
	// tableau d'historique en dessous sert de vue tabulaire.
	import { bytes, dateTime, duration, stateLabel } from '#lib/format.ts';
	import { t } from '#lib/i18n/i18n.svelte.ts';
	import type { Run } from '#lib/types.ts';

	let { runs }: { runs: Run[] } = $props();

	const MAX = 30;
	type Point = { id: string; at: string; state: Run['state']; sec: number; bytes: number };
	const points = $derived(
		runs
			.filter((r) => !r.dryRun && r.startedAt && r.finishedAt)
			.slice(0, MAX)
			.reverse()
			.map(
				(r): Point => ({
					id: r.id,
					at: r.startedAt!,
					state: r.state,
					sec: Math.max(0, (Date.parse(r.finishedAt!) - Date.parse(r.startedAt!)) / 1000),
					bytes: r.bytesDone
				})
			)
	);

	// Maximum « rond » pour des graduations lisibles (0, max/2, max).
	function niceDecimal(v: number): number {
		const p = 10 ** Math.floor(Math.log10(v));
		for (const m of [1, 2, 5, 10]) if (v <= m * p) return m * p;
		return 10 * p;
	}
	const SECONDS = [10, 20, 30, 60, 120, 300, 600, 1200, 1800, 3600, 7200, 14400, 21600, 43200, 86400];
	function niceSeconds(v: number): number {
		return SECONDS.find((s) => v <= s) ?? Math.ceil(v / 86400) * 86400;
	}
	function niceBytes(v: number): number {
		let unit = 1;
		while (v / unit >= 1024) unit *= 1024;
		return niceDecimal(v / unit) * unit;
	}

	const charts = $derived([
		{ key: 'duration', title: t('jobs.charts.duration'), value: (p: Point) => p.sec, fmt: duration, nice: niceSeconds },
		{ key: 'bytes', title: t('jobs.charts.bytes'), value: (p: Point) => p.bytes, fmt: bytes, nice: niceBytes }
	]);
	const niceMax = (c: (typeof charts)[number], v: number) => (v > 0 ? c.nice(v) : 1);

	const H = 140;
	const PAD = { l: 64, r: 8, t: 10, b: 6 };
	let widths = $state<Record<string, number>>({});
	let hover = $state<{ chart: string; i: number } | null>(null);

	// Colonne à extrémité arrondie (4 px), base carrée.
	function column(x: number, y: number, w: number, base: number): string {
		const r = Math.min(4, w / 2, base - y);
		return `M${x},${base}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${base}Z`;
	}
</script>

{#if points.length >= 2}
	<div class="charts">
		{#each charts as c (c.key)}
			{@const max = niceMax(c, Math.max(...points.map(c.value)))}
			{@const plotW = Math.max(0, (widths[c.key] ?? 320) - PAD.l - PAD.r)}
			{@const band = plotW / points.length}
			{@const bw = Math.max(2, Math.min(24, band - 2))}
			{@const base = H - PAD.b}
			{@const y = (v: number) => base - (v / max) * (base - PAD.t)}
			<figure class="card">
				<figcaption>{c.title} <span class="muted small">· {t('jobs.charts.lastRuns', { n: points.length })}</span></figcaption>
				<div class="plot" bind:clientWidth={widths[c.key]}>
					<svg width={plotW + PAD.l + PAD.r} height={H} role="group" aria-label={c.title}>
						{#each [0, max / 2, max] as tick (tick)}
							<line class="grid" x1={PAD.l} x2={PAD.l + plotW} y1={y(tick)} y2={y(tick)} />
							<text class="tick" x={PAD.l - 6} y={y(tick)} dy="0.32em" text-anchor="end">{c.fmt(tick)}</text>
						{/each}
						{#each points as p, i (p.id)}
							{@const x = PAD.l + i * band + (band - bw) / 2}
							{@const v = c.value(p)}
							{#if v > 0}
								<path class="bar" class:on={hover?.chart === c.key && hover.i === i} d={column(x, y(v), bw, base)} />
							{/if}
							<!-- Point focusable : même valeur au clavier qu'au survol. -->
							<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
							<rect
								class="hit"
								x={PAD.l + i * band}
								y={PAD.t}
								width={band}
								height={base - PAD.t}
								tabindex="0"
								role="graphics-symbol"
								aria-label="{dateTime(p.at)}: {c.fmt(v)} ({stateLabel(p.state)})"
								onpointerenter={() => (hover = { chart: c.key, i })}
								onpointerleave={() => (hover = null)}
								onfocus={() => (hover = { chart: c.key, i })}
								onblur={() => (hover = null)}
							/>
						{/each}
					</svg>
					{#if hover?.chart === c.key}
						{@const p = points[hover.i]}
						{@const left = Math.min(Math.max(PAD.l + (hover.i + 0.5) * band, 80), PAD.l + plotW - 80)}
						<div class="tip" style="left:{left}px" role="status">
							<strong>{c.fmt(c.value(p))}</strong>
							<span class="muted">{dateTime(p.at)} · {stateLabel(p.state)}</span>
						</div>
					{/if}
				</div>
			</figure>
		{/each}
	</div>
{/if}

<style>
	.charts {
		display: grid;
		grid-template-columns: 1fr;
		gap: 1rem;
		margin-bottom: 1rem;
	}
	@media (min-width: 700px) {
		.charts {
			grid-template-columns: 1fr 1fr;
		}
	}
	figure {
		margin: 0;
		min-width: 0;
	}
	figcaption {
		font-weight: 600;
		margin-bottom: 0.4rem;
	}
	.plot {
		position: relative;
		overflow: hidden;
	}
	svg {
		display: block;
		max-width: 100%;
	}
	.grid {
		stroke: var(--border);
		stroke-width: 1;
		shape-rendering: crispEdges;
	}
	.tick {
		fill: var(--muted);
		font-size: 11px;
	}
	.bar {
		fill: var(--accent);
	}
	.bar.on {
		opacity: 0.75;
	}
	.hit {
		fill: transparent;
		outline: none;
		cursor: default;
	}
	.hit:focus-visible {
		stroke: var(--text);
		stroke-width: 1;
	}
	.tip {
		position: absolute;
		top: 0;
		transform: translateX(-50%);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 0.25rem 0.5rem;
		font-size: 0.8rem;
		white-space: nowrap;
		pointer-events: none;
		display: flex;
		flex-direction: column;
		box-shadow: 0 2px 6px rgb(0 0 0 / 0.12);
	}
</style>
