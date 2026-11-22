import { describe, expect, it, vi, afterEach } from 'vitest';
import { api, ApiError, errorMessage, qs, setCsrfToken, setUnauthorizedHandler } from './api.ts';
import { t } from './i18n/i18n.svelte.ts';

function mockFetch(status: number, body: unknown) {
	const fn = vi.fn(async (_url: string, init: RequestInit) => {
		void init;
		return new Response(body === undefined ? null : JSON.stringify(body), { status });
	});
	vi.stubGlobal('fetch', fn);
	return fn;
}

describe('api', () => {
	afterEach(() => vi.unstubAllGlobals());

	it('ajoute le token CSRF sur les mutations uniquement', async () => {
		setCsrfToken('tok');
		const f = mockFetch(200, { ok: true });
		await api('POST', '/jobs', { a: 1 });
		expect((f.mock.calls[0][1].headers as Record<string, string>)['X-CSRF-Token']).toBe('tok');
		await api('GET', '/jobs');
		expect((f.mock.calls[1][1].headers as Record<string, string>)['X-CSRF-Token']).toBeUndefined();
	});

	it('décode les erreurs et leurs champs', async () => {
		mockFetch(400, { error: { code: 'validation', message: 'bad', fields: { 'source.path': 'outside roots' } } });
		const err = (await api('POST', '/jobs', {}).catch((e: unknown) => e)) as ApiError;
		expect(err).toBeInstanceOf(ApiError);
		expect(err.code).toBe('validation');
		expect(errorMessage(err)).toBe(t('lib.api.fieldError', { field: 'source.path', message: 'outside roots' }));
	});

	it('appelle le gestionnaire sur 401 (sauf login)', async () => {
		const h = vi.fn();
		setUnauthorizedHandler(h);
		mockFetch(401, { error: { code: 'unauthenticated', message: 'x' } });
		await api('GET', '/jobs').catch(() => {});
		await api('POST', '/auth/login', {}).catch(() => {});
		expect(h).toHaveBeenCalledTimes(1);
	});

	it('204 renvoie undefined', async () => {
		mockFetch(204, undefined);
		expect(await api('DELETE', '/jobs/1')).toBeUndefined();
	});

	it('qs ignore les valeurs vides', () => {
		expect(qs({ a: 1, b: '', c: undefined, d: 'x y' })).toBe('?a=1&d=x+y');
		expect(qs({})).toBe('');
	});
});
