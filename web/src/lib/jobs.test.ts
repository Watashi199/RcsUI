import { describe, expect, it } from 'vitest';
import { duplicateInput, jobToInput } from './jobs.ts';
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
		const d = duplicateInput(job, 'photos (copy)');
		expect(d.name).toBe('photos (copy)');
		expect(d.schedule).toEqual({ cron: '0 3 * * *', enabled: false });
		(d.options.excludes as string[]).push('*.bak');
		d.source.path = '/other';
		expect(job.options.excludes).toEqual(['*.tmp']);
		expect(job.source.path).toBe('/src');
		expect(duplicateInput(job, 'x'.repeat(150)).name).toHaveLength(100);
	});
});
