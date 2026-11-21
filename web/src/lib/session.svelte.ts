import { get, post, setCsrfToken, ApiError } from './api.ts';
import type { Me, SystemInfo, User } from './types.ts';

// État de session partagé (runes Svelte 5).
export const session = $state<{ user: User | null; info: SystemInfo | null; loaded: boolean }>({
	user: null,
	info: null,
	loaded: false
});

export function applyMe(me: Me) {
	session.user = me.user;
	setCsrfToken(me.csrfToken);
}

/** Charge la session courante ; retourne false si non authentifié. */
export async function loadSession(): Promise<boolean> {
	try {
		applyMe(await get<Me>('/auth/me'));
		if (!session.user?.mustChangePassword) {
			session.info = await get<SystemInfo>('/system/info');
		}
		return true;
	} catch (e) {
		if (e instanceof ApiError && e.status === 401) {
			session.user = null;
			return false;
		}
		throw e;
	} finally {
		session.loaded = true;
	}
}

export async function login(username: string, password: string) {
	applyMe(await post<Me>('/auth/login', { username, password }));
}

export async function logout() {
	try {
		await post('/auth/logout');
	} finally {
		session.user = null;
		session.info = null;
		setCsrfToken('');
	}
}

export const isAdmin = () => session.user?.role === 'admin';
export const canCreateJobs = () => session.user?.role === 'admin' || session.user?.role === 'operator';
