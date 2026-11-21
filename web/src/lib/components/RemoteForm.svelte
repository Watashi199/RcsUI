<script lang="ts">
	import { get, post, put, errorMessage } from '#lib/api.ts';
	import type { ConfigResult, Provider, ProviderOption } from '#lib/types.ts';

	let { name: existing = '', ondone }: { name?: string; ondone: (name: string) => void } = $props();

	let providers = $state<Provider[]>([]);
	let name = $state('');
	let type = $state('');
	let values = $state<Record<string, string>>({});
	let secretSet = $state<Record<string, boolean>>({});
	let showAdvanced = $state(false);
	let filter = $state('');
	let error = $state('');
	let busy = $state(false);
	let question = $state<{ state: string; option: Record<string, unknown> } | null>(null);
	let answer = $state('');

	const provider = $derived(providers.find((p) => p.type === type));
	// Champ "provider" (ex. S3 : AWS, Minio…) : conditionne l'affichage des autres options.
	const subProvider = $derived(values['provider'] ?? '');

	function visible(o: ProviderOption): boolean {
		if (o.advanced && !showAdvanced) return false;
		if (!o.provider) return true;
		const list = o.provider.split(',').map((s) => s.trim());
		if (list[0]?.startsWith('!')) return !list.map((s) => s.slice(1)).includes(subProvider);
		return list.includes(subProvider);
	}
	const shown = $derived(provider ? provider.options.filter(visible) : []);
	const filteredProviders = $derived(
		providers.filter((p) => !filter || p.type.includes(filter.toLowerCase()) || p.description.toLowerCase().includes(filter.toLowerCase()))
	);

	(async () => {
		try {
			providers = (await get<{ items: Provider[] }>('/rclone/providers')).items;
			if (existing) {
				const r = await get<{ name: string; type: string; params: Record<string, unknown> }>('/remotes/' + encodeURIComponent(existing));
				name = r.name;
				type = r.type;
				for (const [k, v] of Object.entries(r.params ?? {})) {
					if (v && typeof v === 'object' && 'secret' in v) secretSet[k] = !!(v as unknown as { set: boolean }).set;
					else values[k] = String(v ?? '');
				}
			}
		} catch (e) {
			error = errorMessage(e);
		}
	})();

	function examplesFor(o: ProviderOption) {
		return (o.examples ?? []).filter((e) => {
			if (!e.provider) return true;
			const list = e.provider.split(',');
			return list[0]?.startsWith('!') ? !list.map((s) => s.slice(1)).includes(subProvider) : list.includes(subProvider);
		});
	}

	function params(): Record<string, unknown> {
		const out: Record<string, unknown> = {};
		for (const o of provider?.options ?? []) {
			const v = values[o.name];
			if (o.secret) {
				if (v) out[o.name] = v;
				else if (existing) out[o.name] = { keep: true };
			} else if (v !== undefined && (v !== '' || existing)) {
				out[o.name] = v;
			}
		}
		return out;
	}

	function handle(r: ConfigResult) {
		if (r.error) error = r.error;
		if (r.done) {
			ondone(name);
			return;
		}
		if (r.question) {
			question = { state: r.state ?? '', option: r.question };
			answer = String((r.question['Default'] as string) ?? '');
		}
	}

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			if (existing) handle(await put<ConfigResult>('/remotes/' + encodeURIComponent(existing), { params: params() }));
			else handle(await post<ConfigResult>('/remotes', { name: name.trim(), type, params: params() }));
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	async function answerQuestion() {
		if (!question) return;
		busy = true;
		try {
			const r = await post<ConfigResult>(`/remotes/${encodeURIComponent(name)}/continue`, { state: question.state, result: answer });
			question = null;
			handle(r);
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

{#if error}<div class="alert err" role="alert">{error}</div>{/if}

{#if question}
	<div class="card">
		<h2>Étape de configuration</h2>
		<p class="help">{question.option['Help'] ?? question.option['Name']}</p>
		{#if String(question.option['Name'] ?? '').includes('token')}
			<div class="alert warn small">
				Ce backend utilise OAuth. Sur un poste disposant d'un navigateur, exécutez
				<code>rclone authorize "{type}"</code> puis collez ici le jeton JSON obtenu.
			</div>
		{/if}
		<div class="field">
			<textarea rows="3" class="mono" bind:value={answer}></textarea>
		</div>
		<button class="primary" onclick={answerQuestion} disabled={busy}>Continuer</button>
	</div>
{:else}
	<form onsubmit={submit} class="form">
		<div class="card">
			<div class="grid2">
				<div class="field">
					<label for="rname">Nom du remote</label>
					<input id="rname" bind:value={name} disabled={!!existing} pattern="[A-Za-z0-9_][A-Za-z0-9_. \-]*" maxlength="64" required />
					<div class="hint">Lettres, chiffres, « _ », « - », « . » et espaces.</div>
				</div>
				<div class="field">
					<label for="rtype">Type</label>
					{#if existing}
						<input id="rtype" value={type} disabled />
					{:else}
						<input placeholder="Filtrer (s3, drive, sftp…)" bind:value={filter} aria-label="Filtrer les types" />
						<select id="rtype" bind:value={type} size="6" required>
							{#each filteredProviders as p (p.type)}<option value={p.type}>{p.type} — {p.description}</option>{/each}
						</select>
					{/if}
				</div>
			</div>
		</div>

		{#if provider}
			<div class="card">
				<div class="row">
					<h2 style="margin:0">Paramètres {provider.type}</h2>
					<span class="spacer"></span>
					<label class="check"><input type="checkbox" bind:checked={showAdvanced} /> Options avancées</label>
				</div>
				{#each shown as o (o.name)}
					{@const ex = examplesFor(o)}
					<div class="field">
						<label for="o-{o.name}">{o.name}{o.required ? ' *' : ''}</label>
						{#if o.secret}
							<input
								id="o-{o.name}"
								type="password"
								autocomplete="new-password"
								bind:value={values[o.name]}
								placeholder={secretSet[o.name] ? '•••••• (inchangé)' : ''}
							/>
						{:else if o.type === 'bool'}
							<select id="o-{o.name}" bind:value={values[o.name]}>
								<option value="">défaut ({String(o.default ?? 'false')})</option>
								<option value="true">true</option>
								<option value="false">false</option>
							</select>
						{:else if o.exclusive && ex.length}
							<select id="o-{o.name}" bind:value={values[o.name]}>
								<option value="">—</option>
								{#each ex as e (e.value)}<option value={e.value}>{e.value}{e.help ? ' — ' + e.help.split('\n')[0] : ''}</option>{/each}
							</select>
						{:else}
							<input
								id="o-{o.name}"
								bind:value={values[o.name]}
								list={ex.length ? `dl-${o.name}` : undefined}
								placeholder={o.default !== undefined && o.default !== '' ? String(o.default) : ''}
							/>
							{#if ex.length}
								<datalist id="dl-{o.name}">
									{#each ex as e (e.value)}<option value={e.value}>{e.help.split('\n')[0]}</option>{/each}
								</datalist>
							{/if}
						{/if}
						<div class="hint">{o.help.split('\n')[0]}</div>
					</div>
				{/each}
			</div>
		{/if}

		<div class="row">
			<span class="spacer"></span>
			<button class="primary" type="submit" disabled={busy || !type}>{existing ? 'Enregistrer' : 'Créer le remote'}</button>
		</div>
	</form>
{/if}

<style>
	.form {
		display: grid;
		gap: 1rem;
		max-width: 900px;
	}
	select[size] {
		margin-top: 0.3rem;
	}
	.help {
		white-space: pre-wrap;
	}
</style>
