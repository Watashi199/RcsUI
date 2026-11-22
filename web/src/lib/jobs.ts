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

/** Copie d'un job : nouveau nom, planification conservée mais désactivée. */
export function duplicateInput(j: Job, name: string): JobInput {
	const input = structuredClone(jobToInput(j));
	input.name = name.slice(0, 100);
	if (input.schedule) input.schedule.enabled = false;
	return input;
}
