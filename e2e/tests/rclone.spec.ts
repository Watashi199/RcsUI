import { expect, test } from '@playwright/test';
import path from 'node:path';
import { Api, dir, asAdmin, expectRunState, readFile, uniq, writeFile } from './helpers.ts';

test('rclone job to a memory remote', async ({ page }) => {
	const api = await Api.admin();
	const remote = uniq('mem').replace(/-/g, '_');
	await api.ok('POST', '/remotes', { name: remote, type: 'memory', params: {} });
	const name = uniq('rclone');
	const src = dir(name);
	writeFile(path.join(src.onHost, 'x.txt'), 'to the cloud');
	const job = await api.ok('POST', '/jobs', {
		name,
		engine: 'rclone',
		mode: 'copy',
		source: { type: 'local', path: src.inContainer },
		destination: { type: 'remote', remote, path: 'bucket/e2e' },
		options: { v: 1 }
	});
	await api.dispose();

	await asAdmin(page);
	await page.goto(`/jobs/${job.id}`);
	await page.getByRole('button', { name: 'Run', exact: true }).click();
	await expectRunState(page, 'Succeeded');
	await page.getByRole('button', { name: 'Changes' }).click();
	await expect(page.locator('body')).toContainText('x.txt');
});

test('bisync job: refused until a resync, then propagates both ways', async ({ page }) => {
	// Docker Desktop (Windows) : les fichiers créés par l'hôte appartiennent à root
	// dans le conteneur, qui tourne non-root et ne peut pas en fixer la date
	// (chtimes). Sous Linux, run.sh aligne PUID/PGID sur l'utilisateur courant.
	test.skip(process.platform === 'win32', 'bind mount ownership on Docker Desktop prevents chtimes');
	const api = await Api.admin();
	const name = uniq('bisync');
	const p1 = dir(name + '/p1');
	const p2 = dir(name + '/p2');
	const old = new Date(Date.now() - 2 * 3600_000);
	writeFile(path.join(p1.onHost, 'one.txt'), 'from path 1', old);
	writeFile(path.join(p2.onHost, 'two.txt'), 'from path 2', old);
	const job = await api.ok('POST', '/jobs', {
		name,
		engine: 'rclone',
		mode: 'bisync',
		source: { type: 'local', path: p1.inContainer },
		destination: { type: 'local', path: p2.inContainer },
		options: { v: 1, bisync: { conflictResolve: 'newer' } }
	});
	await api.dispose();

	await asAdmin(page);
	await page.goto(`/jobs/${job.id}`);

	// Sans listings de référence : échec, avec la consigne dans les logs.
	await page.getByRole('button', { name: 'Run', exact: true }).click();
	await expectRunState(page, 'Failed');
	await page.getByRole('button', { name: 'Logs' }).click();
	await expect(page.locator('body')).toContainText('resync');

	// Resync : fusion des deux côtés, rien n'est supprimé.
	await page.goto(`/jobs/${job.id}`);
	page.once('dialog', (d) => d.accept());
	await page.getByRole('button', { name: 'Resync', exact: true }).click();
	await expectRunState(page, 'Succeeded');
	expect(readFile(path.join(p2.onHost, 'one.txt'))).toBe('from path 1');
	expect(readFile(path.join(p1.onHost, 'two.txt'))).toBe('from path 2');

	// Run normal : un ajout de chaque côté est propagé à l'autre.
	writeFile(path.join(p1.onHost, 'new1.txt'), 'added on 1');
	writeFile(path.join(p2.onHost, 'new2.txt'), 'added on 2');
	await page.goto(`/jobs/${job.id}`);
	await page.getByRole('button', { name: 'Run', exact: true }).click();
	await expectRunState(page, 'Succeeded');
	expect(readFile(path.join(p2.onHost, 'new1.txt'))).toBe('added on 1');
	expect(readFile(path.join(p1.onHost, 'new2.txt'))).toBe('added on 2');
});
