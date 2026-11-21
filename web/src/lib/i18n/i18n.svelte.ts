// Traduction de l'interface (FR / EN), sans dépendance.
//
// Les messages sont rangés par espace de noms dans ./messages/<ns>.ts : chaque
// fichier exporte `fr` (référence) et `en`, typé pour contenir exactement les
// mêmes clés. t() lit la langue courante, un état réactif : un template ou un
// $derived qui appelle t() se met à jour au changement de langue. À l'inverse,
// une constante calculée une seule fois (au niveau module ou script) ne suit
// pas : y stocker des clés et appeler t() au rendu.

import * as common from './messages/common.ts';
import * as jobs from './messages/jobs.ts';
import * as runs from './messages/runs.ts';
import * as admin from './messages/admin.ts';
import * as remotes from './messages/remotes.ts';

export type Locale = 'fr' | 'en';
export const LOCALES: readonly Locale[] = ['fr', 'en'];
export const LOCALE_NAMES: Record<Locale, string> = { fr: 'Français', en: 'English' };

const fr = { ...common.fr, ...jobs.fr, ...runs.fr, ...admin.fr, ...remotes.fr };
export type MessageKey = keyof typeof fr;
const en: Record<MessageKey, string> = { ...common.en, ...jobs.en, ...runs.en, ...admin.en, ...remotes.en };
const catalogs: Record<Locale, Record<MessageKey, string>> = { fr, en };

const STORAGE_KEY = 'rcsui.locale';

function detect(): Locale {
	try {
		const saved = localStorage.getItem(STORAGE_KEY);
		if (saved === 'fr' || saved === 'en') return saved;
	} catch {
		// stockage indisponible (navigation privée, tests) : détection seule
	}
	const nav = typeof navigator !== 'undefined' ? navigator.language : '';
	return nav.toLowerCase().startsWith('fr') ? 'fr' : 'en';
}

export const i18n = $state<{ locale: Locale }>({ locale: detect() });

function applyLang(l: Locale) {
	if (typeof document !== 'undefined') document.documentElement.lang = l;
}
applyLang(i18n.locale);

export function setLocale(l: Locale) {
	i18n.locale = l;
	applyLang(l);
	try {
		localStorage.setItem(STORAGE_KEY, l);
	} catch {
		// préférence non mémorisée
	}
}

export type Params = Record<string, string | number>;

function interpolate(s: string, params?: Params): string {
	if (!params) return s;
	return s.replace(/\{(\w+)\}/g, (m, k: string) => (k in params ? String(params[k]) : m));
}

/** Traduit une clé dans la langue courante ; {nom} est remplacé par params.nom. */
export function t(key: MessageKey, params?: Params): string {
	return interpolate(catalogs[i18n.locale][key] ?? key, params);
}

/**
 * Accord en nombre : `one` pour un singulier (FR : 0 et 1 ; EN : 1), `other`
 * sinon. {n} est disponible dans les deux messages.
 */
export function tn(n: number, one: MessageKey, other: MessageKey, params?: Params): string {
	const singular = i18n.locale === 'fr' ? Math.abs(n) < 2 : Math.abs(n) === 1;
	return t(singular ? one : other, { n, ...params });
}

/** Étiquette BCP 47 pour Intl (dates, nombres). */
export function intlLocale(): string {
	return i18n.locale === 'fr' ? 'fr-FR' : 'en-GB';
}

/** Pour les tests : toutes les clés d'une langue. */
export function catalog(l: Locale): Readonly<Record<MessageKey, string>> {
	return catalogs[l];
}
