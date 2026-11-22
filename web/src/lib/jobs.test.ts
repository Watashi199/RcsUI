import { describe, expect, it } from 'vitest';
import { duplicateInput, jobToInput, truncateBytes } from './jobs.ts';
import type { Job } from './types.ts';

const job: Job = {
	id: 7,
	name: 'photos',
	engine: 'rsync',
	mode: 'sync',
	source: { type: 'local', path: '/src', contentsOnly: true },
	destination: { type: 'local', path: '/dst' },
	options: { v: 1, excludes: ['*.tmp'] },
	scheduleCron: '0 3 * * *',
	scheduleEnabled: true,
	retryInterrupted: true,
	retryMax: 2,
	retryDelaySec: 60,
	catchUp: true,
	exclusiveDestination: true,
	createdAt: '',
	updatedAt: '',
	permission: 'edit',
	active: false
};

describe('jobs', () => {
	it('maps a job to form input', () => {
		const i = jobToInput(job);
		expect(i.schedule).toEqual({ cron: '0 3 * * *', enabled: true });
		expect(i.exclusiveDestination).toBe(true);
		expect(i.retryMax).toBe(2);
	});

	it('duplicates with a new name and the schedule disabled, without aliasing', () => {
		const copy = (n: string) => `${n} (copy)`;
		const d = duplicateInput(job, copy);
		expect(d.name).toBe('photos (copy)');
		expect(d.schedule).toEqual({ cron: '0 3 * * *', enabled: false });
		(d.options.excludes as string[]).push('*.bak');
		d.source.path = '/other';
		expect(job.options.excludes).toEqual(['*.tmp']);
		expect(job.source.path).toBe('/src');
		// Nom d'origine à la limite : le suffixe reste, le total tient en 100 octets UTF-8.
		const long = duplicateInput({ ...job, name: 'x'.repeat(100) }, copy).name;
		expect(long.endsWith(' (copy)')).toBe(true);
		expect(long).toHaveLength(100);
		const accents = duplicateInput({ ...job, name: 'é'.repeat(50) }, copy).name;
		expect(accents.endsWith(' (copy)')).toBe(true);
		expect(new TextEncoder().encode(accents).length).toBeLessThanOrEqual(100);
		expect(truncateBytes('aé', 2)).toBe('a');
	});
});
