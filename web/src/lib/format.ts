import type { RunState } from './types.ts';

const UNITS = ['o', 'Kio', 'Mio', 'Gio', 'Tio', 'Pio'];

export function bytes(n: number | undefined | null): string {
	if (n === undefined || n === null || !isFinite(n)) return '—';
	let i = 0;
	let v = n;
	while (v >= 1024 && i < UNITS.length - 1) {
		v /= 1024;
		i++;
	}
	return `${i === 0 ? v : v.toFixed(v < 10 ? 2 : 1)} ${UNITS[i]}`;
}

export function speed(bps: number | undefined): string {
	return bps ? `${bytes(bps)}/s` : '—';
}

export function duration(sec: number | undefined | null): string {
	if (sec === undefined || sec === null || sec < 0 || !isFinite(sec)) return '—';
	sec = Math.round(sec);
	const h = Math.floor(sec / 3600);
	const m = Math.floor((sec % 3600) / 60);
	const s = sec % 60;
	if (h) return `${h} h ${String(m).padStart(2, '0')} min`;
	if (m) return `${m} min ${String(s).padStart(2, '0')} s`;
	return `${s} s`;
}

export function between(a?: string, b?: string): string {
	if (!a) return '—';
	const end = b ? new Date(b).getTime() : Date.now();
	return duration((end - new Date(a).getTime()) / 1000);
}

const dtf = new Intl.DateTimeFormat('fr-FR', { dateStyle: 'short', timeStyle: 'medium' });

export function dateTime(s?: string | number): string {
	if (s === undefined || s === '') return '—';
	const d = new Date(s);
	return isNaN(d.getTime()) ? '—' : dtf.format(d);
}

export function relative(s?: string): string {
	if (!s) return '—';
	const diff = (new Date(s).getTime() - Date.now()) / 1000;
	const rtf = new Intl.RelativeTimeFormat('fr', { numeric: 'auto' });
	const abs = Math.abs(diff);
	if (abs < 60) return rtf.format(Math.round(diff), 'second');
	if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute');
	if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour');
	return rtf.format(Math.round(diff / 86400), 'day');
}

export const STATE_LABEL: Record<RunState, string> = {
	queued: 'En file',
	starting: 'Démarrage',
	running: 'En cours',
	cancelling: 'Annulation…',
	succeeded: 'Réussi',
	partial: 'Partiel',
	failed: 'Échec',
	cancelled: 'Annulé',
	interrupted: 'Interrompu',
	skipped: 'Ignoré'
};

export function stateTone(s: RunState): 'ok' | 'warn' | 'err' | 'info' | 'muted' {
	switch (s) {
		case 'succeeded':
			return 'ok';
		case 'partial':
		case 'cancelled':
		case 'interrupted':
			return 'warn';
		case 'failed':
			return 'err';
		case 'skipped':
			return 'muted';
		default:
			return 'info';
	}
}

export function percent(done: number, total: number): number | null {
	if (!total || total <= 0) return null;
	return Math.min(100, (done / total) * 100);
}
