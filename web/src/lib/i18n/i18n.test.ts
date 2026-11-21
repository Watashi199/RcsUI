import { describe, expect, it } from 'vitest';
import { catalog, setLocale, t, tn, type MessageKey } from './i18n.svelte.ts';

const vars = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();

describe('i18n', () => {
	it('uses the same placeholders in every language', () => {
		const fr = catalog('fr');
		const en = catalog('en');
		const bad: string[] = [];
		for (const k of Object.keys(fr) as MessageKey[]) {
			if (!en[k]?.trim()) bad.push(`${k}: missing English text`);
			else if (vars(fr[k]).join() !== vars(en[k]).join()) bad.push(`${k}: ${vars(fr[k])} vs ${vars(en[k])}`);
		}
		expect(bad).toEqual([]);
	});

	it('interpolates and switches language', () => {
		setLocale('fr');
		expect(t('common.serverUnreachable', { error: 'x' })).toBe('Serveur injoignable : x');
		setLocale('en');
		expect(t('common.serverUnreachable', { error: 'x' })).toBe('Server unreachable: x');
	});

	it('picks the plural form per language', () => {
		const keys = Object.keys(catalog('fr')).filter((k) => k.endsWith('.one')) as MessageKey[];
		expect(keys.length).toBeGreaterThan(0);
		const one = keys[0];
		const other = one.replace(/\.one$/, '.other') as MessageKey;
		setLocale('fr');
		expect(tn(0, one, other)).toBe(t(one, { n: 0 }));
		setLocale('en');
		expect(tn(0, one, other)).toBe(t(other, { n: 0 }));
		expect(tn(1, one, other)).toBe(t(one, { n: 1 }));
	});
});
