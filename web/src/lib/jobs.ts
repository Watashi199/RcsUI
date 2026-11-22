// Conversion d'un job lu par l'API en entrée de formulaire (édition, duplication).

import type { Job, JobInput } from './types.ts';

export function jobToInput(j: Job): JobInput {
	return {
		name: j.name,
		engine: j.engine,
		mode: j.mode,
		source: j.source,
		destination: j.destination,
		options: j.options ?? { v: 1 },
		schedule: j.scheduleCron ? { cron: j.scheduleCron, enabled: j.scheduleEnabled } : null,
		retryInterrupted: j.retryInterrupted,
		retryMax: j.retryMax ?? 0,
		retryDelaySec: j.retryDelaySec || 300,
		catchUp: j.catchUp ?? false,
		exclusiveDestination: j.exclusiveDestination ?? false
	};
}

/** Longueur maximale d'un nom de job côté serveur, en octets UTF-8. */
export const MAX_NAME_BYTES = 100;

const utf8Len = (s: string) => new TextEncoder().encode(s).length;

/** Tronque s à max octets UTF-8 sans couper un caractère. */
export function truncateBytes(s: string, max: number): string {
	let out = '';
	for (const ch of s) {
		if (utf8Len(out + ch) > max) break;
		out += ch;
	}
	return out;
}

/**
 * Copie d'un job : nom produit par copyName (ex. « {nom} (copie) »), dont le
 * suffixe est toujours conservé en tronquant le nom d'origine ; planification
 * conservée mais désactivée.
 */
export function duplicateInput(j: Job, copyName: (base: string) => string): JobInput {
	const input = structuredClone(jobToInput(j));
	const room = MAX_NAME_BYTES - utf8Len(copyName(''));
	input.name = copyName(truncateBytes(j.name, Math.max(0, room)).trimEnd());
	if (input.schedule) input.schedule.enabled = false;
	return input;
}
