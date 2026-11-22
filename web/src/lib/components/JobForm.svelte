<script lang="ts">
	import { untrack } from 'svelte';
	import { get, post, qs, errorMessage, ApiError } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import { dateTime } from '#lib/format.ts';
	import type { Endpoint, EngineKind, JobInput, RemoteSummary, SSHIdentityInfo } from '#lib/types.ts';
	import { isAdmin } from '#lib/session.svelte.ts';
	import { t, type MessageKey } from '#lib/i18n/i18n.svelte.ts';
	import PathPicker from './PathPicker.svelte';

	let {
		initial,
		submitLabel,
		onsubmit
	}: { initial: JobInput; submitLabel: string; onsubmit: (j: JobInput) => Promise<void> } = $props();

	// Copie profonde, prise une seule fois : le formulaire édite son propre état.
	const init = untrack(() => structuredClone($state.snapshot(initial)) as JobInput);
	let job = $state<JobInput>(init);
	let cronOn = $state(!!init.schedule?.cron);
	let cron = $state(init.schedule?.cron ?? '0 3 * * *');
	let cronEnabled = $state(init.schedule?.enabled ?? true);
	let excludes = $state(((init.options.excludes as string[]) ?? []).join('\n'));
	let includes = $state(((init.options.includes as string[]) ?? []).join('\n'));

	let remotes = $state<RemoteSummary[]>([]);
	let fields = $state<Record<string, string>>({});
	let error = $state('');
	let busy = $state(false);
	let preview = $state<{ argv?: string[]; rpc?: string; input?: unknown } | null>(null);
	let nextRuns = $state<string[]>([]);
	let cronError = $state('');
	let picker = $state<{ side: 'source' | 'destination' } | null>(null);

	let identities = $state<SSHIdentityInfo[]>([]);
	let sshTest = $state<Record<string, string>>({});

	if (session.info?.rcloneEnabled) {
		get<{ items: RemoteSummary[] }>('/remotes')
			.then((r) => (remotes = r.items))
			.catch(() => {});
	}
	if (session.info?.sshAvailable) {
		get<{ items: SSHIdentityInfo[] }>('/ssh/identities')
			.then((r) => (identities = r.items))
			.catch(() => {});
	}

	function identityValue(ep: Endpoint): string {
		const id = ep.ssh?.identity;
		return id?.mounted ? 'mounted' : id?.managed ? String(id.managed) : '';
	}
	function setIdentity(side: 'source' | 'destination', v: string) {
		const ssh = job[side].ssh;
		if (!ssh) return;
		ssh.identity = v === 'mounted' ? { mounted: true } : v ? { managed: Number(v) } : {};
	}
	async function testSSH(side: 'source' | 'destination') {
		const ssh = job[side].ssh;
		if (!ssh) return;
		sshTest[side] = t('jobs.form.ssh.testing');
		try {
			const r = await post<{ ok: boolean; error?: string; output?: string }>('/ssh/test', $state.snapshot(ssh));
			sshTest[side] = r.ok ? t('jobs.form.ssh.ok') : `✘ ${r.error ?? t('jobs.form.ssh.failed')}${r.output ? ' — ' + r.output : ''}`;
		} catch (e) {
			sshTest[side] = '✘ ' + errorMessage(e);
		}
	}

	const engines = $derived(
		[
			session.info?.rsyncVersion ? ('rsync' as EngineKind) : null,
			session.info?.rcloneEnabled ? ('rclone' as EngineKind) : null
		].filter(Boolean) as EngineKind[]
	);
	const endpointTypes = $derived<readonly Endpoint['type'][]>(
		job.engine === 'rclone' ? ['local', 'remote'] : session.info?.sshAvailable ? ['local', 'ssh'] : ['local']
	);
	const TYPE_LABEL: Record<Endpoint['type'], MessageKey> = {
		local: 'jobs.form.type.local',
		remote: 'jobs.form.type.remote',
		ssh: 'jobs.form.type.ssh'
	};

	// Options booléennes exposées par moteur (liste blanche côté serveur).
	const RSYNC_FLAGS: [string, MessageKey][] = [
		['checksum', 'jobs.form.flag.rsync.checksum'],
		['compress', 'jobs.form.flag.rsync.compress'],
		['partial', 'jobs.form.flag.rsync.partial'],
		['hardLinks', 'jobs.form.flag.rsync.hardLinks'],
		['sparse', 'jobs.form.flag.rsync.sparse'],
		['update', 'jobs.form.flag.rsync.update'],
		['ignoreExisting', 'jobs.form.flag.ignoreExisting'],
		['oneFileSystem', 'jobs.form.flag.rsync.oneFileSystem'],
		['sizeOnly', 'jobs.form.flag.sizeOnly'],
		['fakeSuper', 'jobs.form.flag.rsync.fakeSuper'],
		['acls', 'jobs.form.flag.rsync.acls'],
		['xattrs', 'jobs.form.flag.rsync.xattrs'],
		['deleteExcluded', 'jobs.form.flag.deleteExcluded'],
		['accurateProgress', 'jobs.form.flag.rsync.accurateProgress']
	];
	const RCLONE_FLAGS: [string, MessageKey][] = [
		['checksum', 'jobs.form.flag.checksum'],
		['sizeOnly', 'jobs.form.flag.sizeOnly'],
		['update', 'jobs.form.flag.update'],
		['ignoreExisting', 'jobs.form.flag.ignoreExisting'],
		['trackRenames', 'jobs.form.flag.trackRenames'],
		['createEmptySrcDirs', 'jobs.form.flag.createEmptySrcDirs'],
		['deleteExcluded', 'jobs.form.flag.deleteExcluded']
	];
	// En bisync, seules ces options génériques ont un sens (le serveur refuse les autres).
	const BISYNC_FLAGS = new Set(['createEmptySrcDirs']);
	const isBisync = $derived(job.mode === 'bisync');
	const flags = $derived(
		job.engine === 'rclone' ? RCLONE_FLAGS.filter(([k]) => !isBisync || BISYNC_FLAGS.has(k)) : RSYNC_FLAGS
	);

	// Options du mode bisync (options.bisync).
	type BisyncOpts = Record<string, string | number | boolean | undefined>;
	function bopts(): BisyncOpts {
		return (job.options.bisync ??= {}) as BisyncOpts;
	}
	function bval(key: string): string {
		const v = (job.options.bisync as BisyncOpts | undefined)?.[key];
		return v === undefined ? '' : String(v);
	}
	function setB(key: string, v: string | number | boolean) {
		const b = bopts();
		if (v === '' || v === false) delete b[key];
		else b[key] = v;
	}
	const COMPARE = ['size', 'modtime', 'checksum'] as const;
	const COMPARE_LABEL: Record<string, MessageKey> = {
		size: 'jobs.form.bisync.compare.size',
		modtime: 'jobs.form.bisync.compare.modtime',
		checksum: 'jobs.form.bisync.compare.checksum'
	};
	function compareHas(c: string): boolean {
		return bval('compare').split(',').includes(c);
	}
	function toggleCompare(c: string, on: boolean) {
		const cur = new Set(bval('compare').split(',').filter(Boolean));
		if (on) cur.add(c);
		else cur.delete(c);
		setB('compare', COMPARE.filter((x) => cur.has(x)).join(','));
	}

	// Options génériques refusées par le serveur en bisync.
	const NOT_BISYNC = ['checksum', 'sizeOnly', 'update', 'ignoreExisting', 'trackRenames', 'deleteExcluded',
		'deleteEmptySrcDirs', 'maxDelete', 'maxAge', 'minAge', 'maxSize', 'minSize'];
	function setMode(m: JobInput['mode']) {
		job.mode = m;
		if (m === 'bisync') for (const k of NOT_BISYNC) delete job.options[k];
		else delete job.options.bisync;
	}

	function setEngine(e: EngineKind) {
		job.engine = e;
		job.options = { v: 1 };
		if (e === 'rsync') {
			if (job.mode === 'move' || job.mode === 'bisync') job.mode = 'copy';
			for (const side of ['source', 'destination'] as const) {
				if (job[side].type !== 'local') job[side] = { type: 'local', path: '' };
			}
		}
	}

	function setType(side: 'source' | 'destination', t: Endpoint['type']) {
		if (t === 'remote') job[side] = { type: 'remote', remote: remotes[0]?.name ?? '', path: '' };
		else if (t === 'ssh')
			job[side] = {
				type: 'ssh',
				path: '',
				ssh: { host: '', port: 22, user: '', identity: identities[0] ? { managed: identities[0].id } : {} }
			};
		else job[side] = { type: 'local', path: '' };
	}

	function lines(s: string): string[] {
		return s
			.split('\n')
			.map((x) => x.trim())
			.filter(Boolean);
	}

	function numOpt(key: string): number | '' {
		const v = job.options[key];
		return typeof v === 'number' ? v : '';
	}
	function setNum(key: string, v: string) {
		if (v === '') delete job.options[key];
		else job.options[key] = Number(v);
	}

	function build(): JobInput {
		const opts: Record<string, unknown> = { ...$state.snapshot(job.options), v: 1 };
		const ex = lines(excludes);
		const inc = lines(includes);
		if (ex.length) opts.excludes = ex;
		else delete opts.excludes;
		if (inc.length) opts.includes = inc;
		else delete opts.includes;
		for (const [k, v] of Object.entries(opts)) if (v === false || v === '' || v === null) delete opts[k];
		if (job.mode !== 'bisync') delete opts.bisync;
		return {
			...$state.snapshot(job),
			options: opts,
			schedule: cronOn ? { cron: cron.trim(), enabled: cronEnabled } : null
		};
	}

	function handleError(e: unknown) {
		fields = e instanceof ApiError ? e.fields : {};
		error = Object.keys(fields).length ? t('jobs.form.invalid') : errorMessage(e);
	}

	async function doPreview() {
		error = '';
		fields = {};
		try {
			const r = await post<{ preview: typeof preview }>('/jobs/validate', build());
			preview = r.preview;
		} catch (e) {
			preview = null;
			handleError(e);
		}
	}

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		fields = {};
		try {
			await onsubmit(build());
		} catch (err) {
			handleError(err);
		} finally {
			busy = false;
		}
	}

	$effect(() => {
		if (!cronOn) {
			nextRuns = [];
			return;
		}
		const c = cron;
		const t = setTimeout(async () => {
			try {
				const r = await get<{ next: string[] }>('/schedule/preview' + qs({ cron: c }));
				nextRuns = r.next;
				cronError = '';
			} catch (e) {
				nextRuns = [];
				cronError = errorMessage(e);
			}
		}, 300);
		return () => clearTimeout(t);
	});

	const fe = (k: string) => fields[k];
</script>

<form onsubmit={submit} class="form">
	{#if error}<div class="alert err" role="alert">{error}</div>{/if}

	<div class="card">
		<div class="grid2">
			<div class="field">
				<label for="name">{t('jobs.form.name')}</label>
				<input id="name" bind:value={job.name} maxlength="100" required />
				{#if fe('name')}<div class="error">{fe('name')}</div>{/if}
			</div>
			<div class="field">
				<label for="engine">{t('jobs.form.engine')}</label>
				<select id="engine" value={job.engine} onchange={(e) => setEngine(e.currentTarget.value as EngineKind)}>
					{#each engines as en (en)}<option value={en}>{en}</option>{/each}
				</select>
			</div>
			<div class="field">
				<label for="mode">{t('jobs.form.mode')}</label>
				<select id="mode" value={job.mode} onchange={(e) => setMode(e.currentTarget.value as JobInput['mode'])}>
					<option value="copy">{t('jobs.form.mode.copy')}</option>
					<option value="sync">{t('jobs.form.mode.sync')}</option>
					{#if job.engine === 'rclone'}
						<option value="move">{t('jobs.form.mode.move')}</option>
						<option value="bisync">{t('jobs.form.mode.bisync')}</option>
					{/if}
				</select>
			</div>
		</div>
	</div>

	{#each ['source', 'destination'] as const as side (side)}
		<div class="card">
			<h2>
				{#if isBisync}{side === 'source' ? t('jobs.form.path1') : t('jobs.form.path2')}{:else}{side === 'source' ? t('jobs.form.source') : t('jobs.form.destination')}{/if}
			</h2>
			<div class="row">
				{#each endpointTypes as ty (ty)}
					<label class="check"
						><input type="radio" name="{side}-type" checked={job[side].type === ty} onchange={() => setType(side, ty)} />
						{t(TYPE_LABEL[ty])}</label
					>
				{/each}
			</div>
			{#if job[side].type === 'ssh' && job[side].ssh}
				{@const ssh = job[side].ssh}
				<div class="sshgrid">
					<div class="field">
						<label for="{side}-host">{t('jobs.form.ssh.host')}</label>
						<input id="{side}-host" bind:value={ssh.host} placeholder="nas2.lan" />
					</div>
					<div class="field">
						<label for="{side}-port">{t('jobs.form.ssh.port')}</label>
						<input id="{side}-port" type="number" min="1" max="65535" bind:value={ssh.port} />
					</div>
					<div class="field">
						<label for="{side}-user">{t('jobs.form.ssh.user')}</label>
						<input id="{side}-user" bind:value={ssh.user} placeholder="backup" />
					</div>
					<div class="field">
						<label for="{side}-id">{t('jobs.form.ssh.identity')}</label>
						<select id="{side}-id" value={identityValue(job[side])} onchange={(e) => setIdentity(side, e.currentTarget.value)}>
							<option value="">—</option>
							{#each identities as i (i.id)}<option value={String(i.id)}>{i.name} ({i.keyType})</option>{/each}
							{#if isAdmin() && session.info?.sshMountedAvailable}<option value="mounted">{t('jobs.form.ssh.mounted')}</option>{/if}
						</select>
					</div>
				</div>
				{#if fe(side + '.ssh') || fe(side + '.ssh.identity')}
					<div class="error small">{fe(side + '.ssh') ?? fe(side + '.ssh.identity')}</div>
				{/if}
				<div class="row small">
					<button type="button" onclick={() => testSSH(side)} disabled={!ssh.host || !ssh.user}>{t('jobs.form.ssh.test')}</button>
					{#if sshTest[side]}<span class="mono">{sshTest[side]}</span>{/if}
					{#if isAdmin()}<a href="/ssh">{t('jobs.form.ssh.manage')}</a>{/if}
				</div>
			{/if}
			<div class="row endpoint">
				{#if job[side].type === 'remote'}
					<select bind:value={job[side].remote} aria-label={t('jobs.form.remote')}>
						{#each remotes as r (r.name)}<option value={r.name}>{r.name} ({r.type})</option>{/each}
					</select>
					<span>:</span>
				{/if}
				<input
					class="grow mono"
					bind:value={job[side].path}
					placeholder={job[side].type === 'remote' ? t('jobs.form.placeholder.remote') : job[side].type === 'ssh' ? t('jobs.form.placeholder.ssh') : '/mnt/...'}
					aria-label={t('jobs.form.path')}
				/>
				{#if job[side].type !== 'ssh'}
					<button type="button" disabled={job[side].type === 'remote' && !job[side].remote} onclick={() => (picker = { side })}
						>{t('jobs.form.browse')}</button
					>
				{/if}
			</div>
			{#if fe(side + '.path') || fe(side) || fe(side + '.remote')}
				<div class="error small">{fe(side + '.path') ?? fe(side) ?? fe(side + '.remote')}</div>
			{/if}
			{#if side === 'source' && job.engine === 'rsync'}
				<label class="check"
					><input type="checkbox" bind:checked={job.source.contentsOnly} /> {t('jobs.form.contentsOnly.before')}
					<em>{t('jobs.form.contentsOnly.em')}</em> {t('jobs.form.contentsOnly.after')}</label
				>
			{/if}
		</div>
	{/each}

	<div class="card">
		<h2>{t('jobs.form.options')}</h2>
		<div class="flags">
			{#each flags as [key, label] (key)}
				<label class="check"><input type="checkbox" checked={!!job.options[key]} onchange={(e) => (job.options[key] = e.currentTarget.checked)} /> {t(label)}</label>
			{/each}
		</div>
		<div class="grid2" style="margin-top:0.8rem">
			{#if job.engine === 'rsync'}
				{#if job.mode === 'sync'}
					<div class="field">
						<label for="del">{t('jobs.form.deleteTiming')}</label>
						<select id="del" value={job.options.delete ?? 'after'} onchange={(e) => (job.options.delete = e.currentTarget.value)}>
							<option value="after">{t('jobs.form.deleteTiming.after')}</option>
							<option value="during">{t('jobs.form.deleteTiming.during')}</option>
							<option value="delay">{t('jobs.form.deleteTiming.delay')}</option>
						</select>
					</div>
				{/if}
				<div class="field">
					<label for="bw">{t('jobs.form.bwLimit')}</label>
					<input id="bw" type="number" min="0" value={numOpt('bwLimitKiB')} oninput={(e) => setNum('bwLimitKiB', e.currentTarget.value)} />
				</div>
				<div class="field">
					<label for="to">{t('jobs.form.ioTimeout')}</label>
					<input id="to" type="number" min="0" value={numOpt('timeoutSec')} oninput={(e) => setNum('timeoutSec', e.currentTarget.value)} />
				</div>
			{:else}
				<div class="field">
					<label for="tr">{t('jobs.form.transfers')}</label>
					<input id="tr" type="number" min="1" max="64" value={numOpt('transfers')} oninput={(e) => setNum('transfers', e.currentTarget.value)} />
				</div>
				<div class="field">
					<label for="ck">{t('jobs.form.checkers')}</label>
					<input id="ck" type="number" min="1" max="128" value={numOpt('checkers')} oninput={(e) => setNum('checkers', e.currentTarget.value)} />
				</div>
				{#if !isBisync}
					<div class="field">
						<label for="ma">{t('jobs.form.maxAge')}</label>
						<input id="ma" value={job.options.maxAge ?? ''} oninput={(e) => (job.options.maxAge = e.currentTarget.value)} />
					</div>
				{/if}
			{/if}
			{#if job.mode !== 'copy' && !isBisync}
				<div class="field">
					<label for="md">{t('jobs.form.maxDelete')}</label>
					<input id="md" type="number" min="0" value={numOpt('maxDelete')} oninput={(e) => setNum('maxDelete', e.currentTarget.value)} />
				</div>
			{/if}
		</div>
		<div class="grid2">
			<div class="field">
				<label for="ex">{t('jobs.form.excludes')}</label>
				<textarea id="ex" rows="4" class="mono" bind:value={excludes} placeholder="*.tmp&#10;.DS_Store"></textarea>
			</div>
			<div class="field">
				<label for="in">{t('jobs.form.includes')}</label>
				<textarea id="in" rows="4" class="mono" bind:value={includes}></textarea>
			</div>
		</div>
		{#each Object.entries(fields).filter(([k]) => k.startsWith('options')) as [k, v] (k)}
			<div class="error small">{t('jobs.form.optionError', { key: k, msg: v })}</div>
		{/each}
	</div>

	{#if isBisync}
		<div class="card">
			<h2>{t('jobs.form.bisync.title')}</h2>
			<div class="alert warn small">
				{t('jobs.form.bisync.intro1')}
				<strong>{t('jobs.form.bisync.intro2')}</strong> {t('jobs.form.bisync.intro3')}
			</div>
			<div class="grid2">
				<div class="field">
					<label for="bcr">{t('jobs.form.bisync.conflict')}</label>
					<select id="bcr" value={bval('conflictResolve')} onchange={(e) => setB('conflictResolve', e.currentTarget.value)}>
						<option value="">{t('jobs.form.bisync.keepBoth')}</option>
						<option value="newer">{t('jobs.form.bisync.win.newer')}</option>
						<option value="older">{t('jobs.form.bisync.win.older')}</option>
						<option value="larger">{t('jobs.form.bisync.win.larger')}</option>
						<option value="smaller">{t('jobs.form.bisync.win.smaller')}</option>
						<option value="path1">{t('jobs.form.bisync.win.path1')}</option>
						<option value="path2">{t('jobs.form.bisync.win.path2')}</option>
					</select>
				</div>
				<div class="field">
					<label for="bcl">{t('jobs.form.bisync.loser')}</label>
					<select id="bcl" value={bval('conflictLoser')} onchange={(e) => setB('conflictLoser', e.currentTarget.value)}>
						<option value="">{t('jobs.form.bisync.loser.num')}</option>
						<option value="pathname">{t('jobs.form.bisync.loser.pathname')}</option>
						<option value="delete">{t('common.delete')}</option>
					</select>
				</div>
				<div class="field">
					<label for="bcs">{t('jobs.form.bisync.suffix')}</label>
					<input id="bcs" class="mono" placeholder="conflict" value={bval('conflictSuffix')}
						oninput={(e) => setB('conflictSuffix', e.currentTarget.value.trim())} />
				</div>
				<div class="field">
					<label for="brm">{t('jobs.form.bisync.resyncMode')}</label>
					<select id="brm" value={bval('resyncMode')} onchange={(e) => setB('resyncMode', e.currentTarget.value)}>
						<option value="">{t('jobs.form.bisync.win.path1Default')}</option>
						<option value="path2">{t('jobs.form.bisync.win.path2')}</option>
						<option value="newer">{t('jobs.form.bisync.win.newer')}</option>
						<option value="older">{t('jobs.form.bisync.win.older')}</option>
						<option value="larger">{t('jobs.form.bisync.win.larger')}</option>
						<option value="smaller">{t('jobs.form.bisync.win.smaller')}</option>
					</select>
				</div>
				<div class="field">
					<label for="bmd">{t('jobs.form.bisync.maxDeletePercent')}</label>
					<input id="bmd" type="number" min="0" max="100" value={bval('maxDeletePercent')}
						oninput={(e) => setB('maxDeletePercent', e.currentTarget.value === '' ? '' : Number(e.currentTarget.value))} />
				</div>
				<fieldset class="field plain">
					<legend>{t('jobs.form.bisync.compare')}</legend>
					<div class="row">
						{#each COMPARE as c (c)}
							<label class="check"
								><input type="checkbox" checked={compareHas(c)} onchange={(e) => toggleCompare(c, e.currentTarget.checked)} />
								{t(COMPARE_LABEL[c])}</label
							>
						{/each}
					</div>
				</fieldset>
			</div>
			<div class="flags">
				<label class="check"
					><input type="checkbox" checked={!bval('noRecover')} onchange={(e) => setB('noRecover', !e.currentTarget.checked)} />
					{t('jobs.form.bisync.recover')}</label
				>
				<label class="check"
					><input type="checkbox" checked={!!bval('resilient')} onchange={(e) => setB('resilient', e.currentTarget.checked)} />
					{t('jobs.form.bisync.resilient')}</label
				>
				<label class="check"
					><input type="checkbox" checked={!!bval('removeEmptyDirs')} onchange={(e) => setB('removeEmptyDirs', e.currentTarget.checked)} />
					{t('jobs.form.bisync.removeEmptyDirs')}</label
				>
				<label class="check"
					><input type="checkbox" checked={!!bval('checkAccess')} onchange={(e) => setB('checkAccess', e.currentTarget.checked)} />
					{t('jobs.form.bisync.checkAccess')}</label
				>
			</div>
		</div>
	{/if}

	<div class="card">
		<h2>{t('jobs.form.schedule')}</h2>
		<label class="check"><input type="checkbox" bind:checked={cronOn} /> {t('jobs.form.schedule.auto')}</label>
		{#if cronOn}
			<div class="grid2">
				<div class="field">
					<label for="cron">{t('jobs.form.schedule.cron')}</label>
					<input id="cron" class="mono" bind:value={cron} />
					{#if cronError || fe('schedule.cron')}<div class="error">{cronError || fe('schedule.cron')}</div>{/if}
					<label class="check"><input type="checkbox" bind:checked={cronEnabled} /> {t('jobs.form.schedule.enabled')}</label>
					<label class="check">
						<input type="checkbox" bind:checked={job.catchUp} />
						{t('jobs.form.schedule.catchUp')}
					</label>
				</div>
				<div class="small muted">
					{#if nextRuns.length}
						{t('jobs.form.schedule.next')}
						<ul>
							{#each nextRuns as n (n)}<li>{dateTime(n)}</li>{/each}
						</ul>
					{/if}
				</div>
			</div>
		{/if}
		<label class="check"><input type="checkbox" bind:checked={job.retryInterrupted} /> {t('jobs.form.retryInterrupted')}</label>
		<label class="check">
			<input type="checkbox" bind:checked={job.exclusiveDestination} />
			{t('jobs.form.exclusiveDestination')}
		</label>
		<p class="small muted">{t('jobs.form.exclusiveDestination.help')}</p>
	</div>

	<div class="card">
		<h2>{t('jobs.form.retries')}</h2>
		<p class="small muted">
			{t('jobs.form.retries.help')}
		</p>
		<div class="grid2">
			<div class="field">
				<label for="rmax">{t('jobs.form.retries.max')}</label>
				<input id="rmax" type="number" min="0" max="10" bind:value={job.retryMax} />
				{#if fe('retryMax')}<div class="error">{fe('retryMax')}</div>{/if}
			</div>
			<div class="field">
				<label for="rdelay">{t('jobs.form.retries.delay')}</label>
				<input id="rdelay" type="number" min="10" max="86400" bind:value={job.retryDelaySec} disabled={!job.retryMax} />
				{#if fe('retryDelaySec')}<div class="error">{fe('retryDelaySec')}</div>{/if}
			</div>
		</div>
	</div>

	{#if preview}
		<div class="card">
			<h2>{t('jobs.form.preview')}</h2>
			{#if preview.argv}
				<pre class="mono">{preview.argv.map((a) => (/[\s"'$]/.test(a) ? JSON.stringify(a) : a)).join(' ')}</pre>
			{:else}
				<pre class="mono">{preview.rpc}
{JSON.stringify(preview.input, null, 2)}</pre>
			{/if}
		</div>
	{/if}

	<div class="row">
		<button type="button" onclick={doPreview}>{t('jobs.form.previewButton')}</button>
		<span class="spacer"></span>
		<button type="submit" class="primary" disabled={busy}>{submitLabel}</button>
	</div>
</form>

{#if picker}
	{@const ep = job[picker.side]}
	<PathPicker
		loc={ep.type === 'remote' ? 'remote' : 'local'}
		remote={ep.remote ?? ''}
		start={ep.path}
		onpick={(p) => {
			if (picker) job[picker.side].path = p;
		}}
		onclose={() => (picker = null)}
	/>
{/if}

<style>
	.form {
		display: grid;
		gap: 1rem;
		max-width: 960px;
	}
	.form h2 {
		margin-top: 0;
	}
	.endpoint {
		flex-wrap: nowrap;
		margin-top: 0.4rem;
	}
	.endpoint select {
		width: auto;
	}
	.grow {
		flex: 1;
	}
	.sshgrid {
		display: grid;
		grid-template-columns: 2fr 0.7fr 1fr 1.5fr;
		gap: 0.6rem;
		margin-top: 0.5rem;
	}
	@media (max-width: 720px) {
		.sshgrid {
			grid-template-columns: 1fr 1fr;
		}
	}
	.flags {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
	}
	.error {
		color: var(--err);
	}
	fieldset.plain {
		border: 0;
		padding: 0;
		margin: 0;
	}
	fieldset.plain legend {
		padding: 0;
	}
	pre {
		white-space: pre-wrap;
		word-break: break-all;
		background: var(--surface-2);
		padding: 0.6rem;
		border-radius: 6px;
		margin: 0;
	}
</style>
