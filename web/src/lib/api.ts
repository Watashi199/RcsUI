// Client HTTP de l'API : ajoute le token CSRF, décode les erreurs typées.

import { t } from './i18n/i18n.svelte.ts';

export class ApiError extends Error {
	constructor(
		public status: number,
		public code: string,
		message: string,
		public fields: Record<string, string> = {}
	) {
		super(message);
	}
}

let csrfToken = '';
let onUnauthorized: (() => void) | null = null;

export function setCsrfToken(t: string) {
	csrfToken = t;
}

export function setUnauthorizedHandler(fn: () => void) {
	onUnauthorized = fn;
}

export async function api<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
	const headers: Record<string, string> = { Accept: 'application/json' };
	if (body !== undefined) headers['Content-Type'] = 'application/json';
	if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken;
	const res = await fetch('/api/v1' + path, {
		method,
		headers,
		body: body === undefined ? undefined : JSON.stringify(body),
		credentials: 'same-origin'
	});
	if (res.status === 204) return undefined as T;
	let data: unknown = null;
	const text = await res.text();
	if (text) {
		try {
			data = JSON.parse(text);
		} catch {
			data = null;
		}
	}
	if (!res.ok) {
		const err = (data as { error?: { code?: string; message?: string; fields?: Record<string, string> } })?.error;
		if (res.status === 401 && onUnauthorized && !path.startsWith('/auth/login')) onUnauthorized();
		throw new ApiError(res.status, err?.code ?? 'http_' + res.status, err?.message ?? res.statusText, err?.fields ?? {});
	}
	return data as T;
}

export const get = <T>(p: string) => api<T>('GET', p);
export const post = <T>(p: string, b?: unknown) => api<T>('POST', p, b ?? {});
export const put = <T>(p: string, b: unknown) => api<T>('PUT', p, b);
export const patch = <T>(p: string, b: unknown) => api<T>('PATCH', p, b);
export const del = <T>(p: string) => api<T>('DELETE', p);

/** Message lisible pour une erreur quelconque. */
export function errorMessage(e: unknown): string {
	if (e instanceof ApiError) {
		const fields = Object.entries(e.fields);
		if (fields.length) return fields.map(([k, v]) => t('lib.api.fieldError', { field: k, message: v })).join(' · ');
		return e.message;
	}
	return e instanceof Error ? e.message : String(e);
}

export function qs(params: Record<string, string | number | boolean | undefined | null>): string {
	const u = new URLSearchParams();
	for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== '') u.set(k, String(v));
	const s = u.toString();
	return s ? '?' + s : '';
}
