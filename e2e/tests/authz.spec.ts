import { expect, test } from '@playwright/test';
import { Api, adoptSession, dir, uniq } from './helpers.ts';

test('an operator without grant cannot see, run, edit or browse a job', async ({ page }) => {
	const admin = await Api.admin();
	const name = uniq('private');
	const src = dir(name + '/src');
	const dst = dir(name + '/dst');
	const job = await admin.ok('POST', '/jobs', {
		name,
		engine: 'rsync',
		mode: 'copy',
		source: { type: 'local', path: src.inContainer },
		destination: { type: 'local', path: dst.inContainer },
		options: { v: 1 }
	});
	const opName = uniq('op');
	await admin.createUser(opName, 'operator', 'initial-password-1');
	await admin.dispose();

	// Activation du compte (changement de mot de passe imposé).
	const op = await Api.login(opName, 'initial-password-1');
	await op.ok('POST', '/auth/password', { current: 'initial-password-1', new: 'operator-password-1' });

	const get = await op.call('GET', `/jobs/${job.id}`);
	expect([403, 404]).toContain(get.status);
	const run = await op.call('POST', `/jobs/${job.id}/run`, {});
	expect([403, 404]).toContain(run.status);
	const edit = await op.call('PUT', `/jobs/${job.id}`, { ...job, name: name + '-x' });
	expect([403, 404]).toContain(edit.status);
	const resync = await op.call('POST', `/jobs/${job.id}/run`, { resync: true });
	expect([403, 404]).toContain(resync.status);

	// Dans l'interface, le job n'apparaît pas (même session, sans nouvelle connexion).
	await adoptSession(page, op);
	await op.dispose();
	await page.goto('/jobs');
	await expect(page.getByRole('heading', { name: 'Jobs' })).toBeVisible();
	await expect(page.getByText(name)).toHaveCount(0);
});

test('a viewer cannot run jobs nor browse the file system', async () => {
	const admin = await Api.admin();
	const vName = uniq('viewer');
	await admin.createUser(vName, 'viewer', 'initial-password-1');
	await admin.dispose();
	const v = await Api.login(vName, 'initial-password-1');
	await v.ok('POST', '/auth/password', { current: 'initial-password-1', new: 'viewer-password-12' });
	const browse = await v.call('GET', '/browse?loc=local&path=/e2e');
	expect(browse.status).toBe(403);
	const create = await v.call('POST', '/jobs', { name: uniq('nope'), engine: 'rsync', mode: 'copy' });
	expect(create.status).toBe(403);
	await v.dispose();
});
