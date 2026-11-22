import { request } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { ADMIN, ADMIN_STATE, BASE_URL } from './tests/helpers.ts';

// Une seule connexion admin pour toute la suite : le serveur limite les
// tentatives de connexion (5, puis une toutes les 12 s par IP). La session
// (cookie) est réutilisée par le navigateur et par le client API.
export default async function globalSetup() {
	const ctx = await request.newContext({ baseURL: BASE_URL });
	const r = await ctx.post('/api/v1/auth/login', { data: { username: ADMIN.user, password: ADMIN.pass } });
	if (r.status() !== 200) throw new Error(`admin login failed: ${r.status()} ${await r.text()}`);
	fs.mkdirSync(path.dirname(ADMIN_STATE), { recursive: true });
	await ctx.storageState({ path: ADMIN_STATE });
	await ctx.dispose();
}
