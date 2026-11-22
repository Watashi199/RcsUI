import { expect, test } from '@playwright/test';
import path from 'node:path';
import { dir, asAdmin, expectRunState, readFile, uniq, writeFile } from './helpers.ts';

test('rsync job: create in the form, dry run, then run', async ({ page }) => {
	const name = uniq('rsync');
	const src = dir(name + '/src');
	const dst = dir(name + '/dst');
	writeFile(path.join(src.onHost, 'a.txt'), 'hello');
	writeFile(path.join(src.onHost, 'sub/b.txt'), 'world');

	await asAdmin(page);
	await page.goto('/jobs/new');
	await page.getByLabel('Name').fill(name);
	await page.getByLabel('Engine').selectOption('rsync');
	await page.getByLabel('Mode').selectOption('copy');
	const paths = page.getByLabel('Path', { exact: true });
	await paths.nth(0).fill(src.inContainer);
	await paths.nth(1).fill(dst.inContainer);
	await page.getByRole('button', { name: 'Create job' }).click();
	await expect(page.getByRole('heading', { name })).toBeVisible();

	// Dry-run : changements prévus, rien d'écrit.
	await page.getByRole('button', { name: 'Dry run', exact: true }).click();
	await expectRunState(page, 'Succeeded');
	expect(readFile(path.join(dst.onHost, 'src/a.txt'))).toBeNull();
	expect(readFile(path.join(dst.onHost, 'a.txt'))).toBeNull();

	// Run réel : les fichiers arrivent, la page du run affiche les logs.
	await page.goBack();
	await page.getByRole('button', { name: 'Run', exact: true }).click();
	await expectRunState(page, 'Succeeded');
	const copied = readFile(path.join(dst.onHost, 'src/a.txt')) ?? readFile(path.join(dst.onHost, 'a.txt'));
	expect(copied).toBe('hello');
	await page.getByRole('button', { name: 'Logs' }).click();
	await expect(page.locator('body')).toContainText('rsync');
});

test('rsync job: a running transfer can be cancelled', async ({ page }) => {
	const name = uniq('cancel');
	const src = dir(name + '/src');
	const dst = dir(name + '/dst');
	// 8 Mio à 64 Kio/s : bien plus long que le test.
	writeFile(path.join(src.onHost, 'big.bin'), Buffer.alloc(8 * 1024 * 1024, 7));

	await asAdmin(page);
	await page.goto('/jobs/new');
	await page.getByLabel('Name').fill(name);
	await page.getByLabel('Engine').selectOption('rsync');
	const paths = page.getByLabel('Path', { exact: true });
	await paths.nth(0).fill(src.inContainer);
	await paths.nth(1).fill(dst.inContainer);
	await page.getByLabel('Bandwidth limit (KiB/s)').fill('64');
	await page.getByRole('button', { name: 'Create job' }).click();
	await expect(page.getByRole('heading', { name })).toBeVisible();

	await page.getByRole('button', { name: 'Run', exact: true }).click();
	await expectRunState(page, 'Running');
	page.once('dialog', (d) => d.accept());
	await page.getByRole('button', { name: 'Cancel', exact: true }).click();
	await expectRunState(page, 'Cancelled');
});
