<script lang="ts">
	import { get, post, put, del, errorMessage } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import { dateTime } from '#lib/format.ts';
	import type { KnownHost, SSHIdentityInfo, ScannedKey, User } from '#lib/types.ts';

	let identities = $state<SSHIdentityInfo[]>([]);
	let hosts = $state<KnownHost[]>([]);
	let operators = $state<User[]>([]);
	let error = $state('');
	let msg = $state('');

	let newName = $state('');
	let importMode = $state(false);
	let privateKey = $state('');
	let passphrase = $state('');

	let scanHost = $state('');
	let scanPort = $state(22);
	let scanned = $state<ScannedKey[] | null>(null);
	let scanning = $state(false);

	let grantsFor = $state<number | null>(null);
	let granted = $state<Record<number, boolean>>({});

	async function load() {
		try {
			const [i, h, u] = await Promise.all([
				get<{ items: SSHIdentityInfo[] }>('/ssh/identities'),
				get<{ items: KnownHost[] }>('/ssh/known-hosts'),
				get<{ items: User[] }>('/users')
			]);
			identities = i.items;
			hosts = h.items;
			operators = u.items.filter((x) => x.role === 'operator');
		} catch (e) {
			error = errorMessage(e);
		}
	}
	void load();

	async function act(fn: () => Promise<unknown>, ok: string) {
		error = '';
		msg = '';
		try {
			await fn();
			msg = ok;
			await load();
		} catch (e) {
			error = errorMessage(e);
		}
	}

	function createIdentity(e: SubmitEvent) {
		e.preventDefault();
		const body = importMode ? { name: newName, privateKey, passphrase } : { name: newName, generate: true };
		void act(
			async () => {
				await post('/ssh/identities', body);
				newName = privateKey = passphrase = '';
			},
			importMode ? 'Clé importée.' : 'Clé générée : ajoutez la clé publique au fichier authorized_keys du serveur distant.'
		);
	}

	async function copy(text: string) {
		try {
			await navigator.clipboard.writeText(text);
			msg = 'Clé publique copiée.';
		} catch {
			error = 'Copie impossible : sélectionnez le texte manuellement.';
		}
	}

	async function openGrants(id: number) {
		grantsFor = id;
		const r = await get<{ userIds: number[] }>(`/ssh/identities/${id}/grants`);
		granted = Object.fromEntries(r.userIds.map((u) => [u, true]));
	}
	function saveGrants() {
		const ids = Object.entries(granted)
			.filter(([, v]) => v)
			.map(([k]) => Number(k));
		void act(() => put(`/ssh/identities/${grantsFor}/grants`, { userIds: ids }), 'Accès enregistrés.').then(
			() => (grantsFor = null)
		);
	}
	function removeIdentity(i: SSHIdentityInfo) {
		if (confirm(`Supprimer l'identité ${i.name} ?`)) void act(() => del('/ssh/identities/' + i.id), 'Identité supprimée.');
	}
	function removeHost(h: KnownHost) {
		if (confirm(`Retirer cette clé de ${h.host} ?`)) void act(() => del('/ssh/known-hosts/' + h.id), 'Clé retirée.');
	}

	async function scan(e: SubmitEvent) {
		e.preventDefault();
		scanning = true;
		error = '';
		scanned = null;
		try {
			scanned = (await post<{ keys: ScannedKey[] }>('/ssh/known-hosts/scan', { host: scanHost.trim(), port: scanPort })).keys;
		} catch (err) {
			error = errorMessage(err);
		} finally {
			scanning = false;
		}
	}
	function approve(k: ScannedKey) {
		void act(
			() => post('/ssh/known-hosts', { host: scanHost.trim(), port: scanPort, key: k.key }),
			`Clé ${k.type} approuvée pour ${scanHost}:${scanPort}.`
		).then(() => (scanned = scanned?.filter((x) => x !== k) ?? null));
	}
</script>

<svelte:head><title>SSH · RcsUI</title></svelte:head>

<h1>SSH</h1>
{#if error}<div class="alert err" role="alert">{error}</div>{/if}
{#if msg}<div class="alert ok">{msg}</div>{/if}
{#if session.info?.sshMountedAvailable}
	<div class="alert warn small">Un dossier ~/.ssh monté est aussi disponible pour les jobs (réservé aux administrateurs).</div>
{/if}

<h2>Identités</h2>
{#if identities.length === 0}
	<div class="card empty">Aucune identité.</div>
{:else}
	<div class="list">
		{#each identities as i (i.id)}
			<div class="card">
				<div class="row">
					<strong>{i.name}</strong>
					<span class="muted small">{i.keyType} · {i.fingerprint} · {dateTime(i.createdAt)}</span>
					<span class="spacer"></span>
					<button onclick={() => copy(i.publicKey)}>Copier la clé publique</button>
					<button onclick={() => openGrants(i.id)}>Accès</button>
					<button class="danger" onclick={() => removeIdentity(i)}>Supprimer</button>
				</div>
				<pre class="mono pub">{i.publicKey}</pre>
				{#if grantsFor === i.id}
					<div class="grants">
						{#if operators.length === 0}<span class="muted small">Aucun utilisateur « operator ».</span>{/if}
						{#each operators as u (u.id)}
							<label class="check"><input type="checkbox" bind:checked={granted[u.id]} /> {u.username}</label>
						{/each}
						<div class="row">
							<button class="primary" onclick={saveGrants}>Enregistrer</button>
							<button onclick={() => (grantsFor = null)}>Fermer</button>
						</div>
					</div>
				{/if}
			</div>
		{/each}
	</div>
{/if}

<form class="card form" onsubmit={createIdentity}>
	<div class="row">
		<label class="check"><input type="radio" checked={!importMode} onchange={() => (importMode = false)} /> Générer (ed25519)</label>
		<label class="check"><input type="radio" checked={importMode} onchange={() => (importMode = true)} /> Importer une clé existante</label>
	</div>
	<div class="field">
		<label for="iname">Nom</label>
		<input id="iname" bind:value={newName} required maxlength="64" />
	</div>
	{#if importMode}
		<div class="field">
			<label for="ipk">Clé privée (PEM / OpenSSH)</label>
			<textarea id="ipk" rows="6" class="mono" bind:value={privateKey} required autocomplete="off" spellcheck="false"></textarea>
			<div class="hint">Stockée chiffrée (AES-256-GCM) ; jamais réaffichée.</div>
		</div>
		<div class="field">
			<label for="ipp">Passphrase (si la clé est protégée)</label>
			<input id="ipp" type="password" autocomplete="off" bind:value={passphrase} />
		</div>
	{/if}
	<button class="primary" type="submit">{importMode ? 'Importer' : 'Générer'}</button>
</form>

<h2>Hôtes connus</h2>
<p class="muted small">
	rsync refuse de se connecter à un hôte dont la clé n'a pas été approuvée ici (pas d'acceptation automatique). Comparez
	l'empreinte affichée avec celle du serveur (<code>ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</code>).
</p>
<form class="card row scan" onsubmit={scan}>
	<input placeholder="hôte ou IP" bind:value={scanHost} required aria-label="Hôte" />
	<input type="number" min="1" max="65535" bind:value={scanPort} aria-label="Port" style="width:7rem" />
	<button type="submit" disabled={scanning}>{scanning ? 'Analyse…' : 'Récupérer les clés'}</button>
</form>
{#if scanned}
	<div class="card">
		{#each scanned as k (k.fingerprint)}
			<div class="row">
				<span class="mono">{k.type}</span>
				<strong class="mono">{k.fingerprint}</strong>
				<span class="spacer"></span>
				<button class="primary" onclick={() => approve(k)}>Approuver</button>
			</div>
		{/each}
	</div>
{/if}
{#if hosts.length}
	<div class="table-wrap" style="margin-top:0.8rem">
		<table>
			<thead><tr><th>Hôte</th><th>Type</th><th>Empreinte</th><th></th></tr></thead>
			<tbody>
				{#each hosts as h (h.id)}
					<tr>
						<td class="mono">{h.host}:{h.port}</td>
						<td class="mono small">{h.keyType}</td>
						<td class="mono small">{h.fingerprint}</td>
						<td style="text-align:right"><button class="danger" onclick={() => removeHost(h)}>Retirer</button></td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}

<style>
	.list {
		display: grid;
		gap: 0.6rem;
		margin-bottom: 1rem;
	}
	.pub {
		white-space: pre-wrap;
		word-break: break-all;
		background: var(--surface-2);
		padding: 0.4rem 0.6rem;
		border-radius: 6px;
		margin: 0.5rem 0 0;
	}
	.grants {
		margin-top: 0.6rem;
	}
	.form {
		max-width: 700px;
	}
	.scan {
		max-width: 700px;
		flex-wrap: nowrap;
	}
</style>
