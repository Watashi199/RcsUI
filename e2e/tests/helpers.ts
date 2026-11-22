import { expect, request, type APIRequestContext, type Page } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

export const BASE_URL = process.env.E2E_BASE_URL ?? 'http://127.0.0.1:18080';
export const ADMIN = { user: 'admin', pass: process.env.E2E_ADMIN_PASSWORD ?? 'e2e-admin-password' };
/** Racine autorisée, vue depuis le conteneur (RCSUI_ALLOWED_ROOTS). */
export const ROOT = process.env.E2E_ROOT ?? '/e2e';
/** Le même dossier, vu depuis la machine qui exécute les tests (bind mount). */
export const HOST_ROOT = process.env.E2E_HOST_ROOT ?? '';
/** Session admin ouverte une fois par global-setup.ts (limite de tentatives de connexion). */
export const ADMIN_STATE = path.join(import.meta.dirname, '..', '.auth', 'admin.json');

/** Nom unique par test, pour que les tests ne se marchent pas dessus. */
export function uniq(prefix: string): string {
	return `${prefix}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
}

/** Chemin conteneur et chemin hôte d'un sous-dossier de la racine. */
export function dir(name: string): { inContainer: string; onHost: string } {
	const onHost = path.join(HOST_ROOT, name);
	fs.mkdirSync(onHost, { recursive: true, mode: 0o777 });
	fs.chmodSync(onHost, 0o777);
	return { inContainer: `${ROOT}/${name}`, onHost };
}

export function writeFile(p: string, content: string | Buffer, mtime?: Date) {
	fs.mkdirSync(path.dirname(p), { recursive: true });
	fs.writeFileSync(p, content);
	fs.chmodSync(p, 0o666);
	if (mtime) fs.utimesSync(p, mtime, mtime);
}

export function readFile(p: string): string | null {
	try {
		return fs.readFileSync(p, 'utf8');
	} catch {
		return null;
	}
}

/** Ouvre l'interface avec la session admin partagée (sans nouvelle connexion). */
export async function asAdmin(page: Page) {
	const state = JSON.parse(fs.readFileSync(ADMIN_STATE, 'utf8'));
	await page.context().addCookies(state.cookies);
	await page.goto('/');
	await expect(page.getByRole('link', { name: 'Dashboard' })).toBeVisible();
}

/** Ouvre l'interface avec la session d'un client API déjà connecté. */
export async function adoptSession(page: Page, api: Api) {
	await page.context().addCookies((await api.ctx.storageState()).cookies);
	await page.goto('/');
}

/** Connexion par le formulaire (consomme une tentative du limiteur : à réserver aux tests de connexion). */
export async function login(page: Page, user = ADMIN.user, pass = ADMIN.pass) {
	await page.goto('/login');
	await page.getByLabel('Username').fill(user);
	await page.getByLabel('Password').fill(pass);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).not.toHaveURL(/\/login/);
}

/** Client API authentifié (cookie de session + jeton CSRF), pour préparer les données. */
export class Api {
	private constructor(
		readonly ctx: APIRequestContext,
		private csrf: string
	) {}

	/** Client admin réutilisant la session partagée ; le jeton CSRF vient de /auth/me. */
	static async admin(): Promise<Api> {
		const ctx = await request.newContext({ baseURL: BASE_URL, storageState: ADMIN_STATE });
		const r = await ctx.get('/api/v1/auth/me');
		expect(r.status(), await r.text()).toBe(200);
		return new Api(ctx, (await r.json()).csrfToken);
	}

	/** Connexion API ; patiente si le limiteur de tentatives répond 429 (Retry-After). */
	static async login(user: string, pass: string): Promise<Api> {
		const ctx = await request.newContext({ baseURL: BASE_URL });
		let r = await ctx.post('/api/v1/auth/login', { data: { username: user, password: pass } });
		for (let i = 0; r.status() === 429 && i < 3; i++) {
			const wait = Number(r.headers()['retry-after'] ?? '12');
			await new Promise((res) => setTimeout(res, (Math.max(wait, 1) + 1) * 1000));
			r = await ctx.post('/api/v1/auth/login', { data: { username: user, password: pass } });
		}
		expect(r.status(), await r.text()).toBe(200);
		const body = await r.json();
		return new Api(ctx, body.csrfToken);
	}

	async call(method: string, url: string, data?: unknown) {
		const r = await this.ctx.fetch('/api/v1' + url, {
			method,
			data,
			headers: { 'X-CSRF-Token': this.csrf }
		});
		const text = await r.text();
		let json: any = null;
		try {
			json = text ? JSON.parse(text) : null;
		} catch {
			json = text;
		}
		return { status: r.status(), json };
	}

	async ok(method: string, url: string, data?: unknown) {
		const r = await this.call(method, url, data);
		expect(r.status, `${method} ${url}: ${JSON.stringify(r.json)}`).toBeLessThan(300);
		return r.json;
	}

	/** Crée un utilisateur. */
	async createUser(username: string, role: 'admin' | 'operator' | 'viewer', password: string) {
		return this.ok('POST', '/users', { username, role, password });
	}

	async dispose() {
		await this.ctx.dispose();
	}
}

/** Attend l'état terminal affiché sur la page d'un run. */
export async function expectRunState(page: Page, state: string) {
	await expect(page).toHaveURL(/\/runs\//);
	await expect(page.locator('.badge').filter({ hasText: new RegExp(`^${state}$`) }).first()).toBeVisible({
		timeout: 45_000
	});
}
