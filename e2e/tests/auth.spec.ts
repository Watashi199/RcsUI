import { expect, test } from '@playwright/test';
import { Api, asAdmin, login, uniq } from './helpers.ts';

test('admin signs in and sees the dashboard', async ({ page }) => {
	await login(page);
	await expect(page.getByRole('link', { name: 'Dashboard' })).toBeVisible();
	await expect(page.getByRole('link', { name: 'Users' })).toBeVisible();
});

test('wrong password is refused', async ({ page }) => {
	await page.goto('/login');
	await page.getByLabel('Username').fill('admin');
	await page.getByLabel('Password').fill('not-the-password');
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page.getByRole('alert')).toBeVisible();
	await expect(page).toHaveURL(/\/login/);
});

test('a new user must change the initial password', async ({ page }) => {
	const api = await Api.admin();
	const name = uniq('newbie');
	await api.createUser(name, 'viewer', 'initial-password-1');
	await api.dispose();

	await page.goto('/login');
	await page.getByLabel('Username').fill(name);
	await page.getByLabel('Password').fill('initial-password-1');
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).toHaveURL(/\/password/);
	await expect(page.getByText('You must choose a new password before continuing.')).toBeVisible();

	await page.getByLabel('Current password').fill('initial-password-1');
	await page.getByLabel('New password').fill('a-brand-new-password');
	await page.getByLabel('Confirm password').fill('a-brand-new-password');
	await page.getByRole('button', { name: 'Save' }).click();
	await expect(page.getByRole('link', { name: 'Dashboard' })).toBeVisible();
});

test('the interface switches to French and remembers it', async ({ page }) => {
	await asAdmin(page);
	await page.getByLabel('Language').selectOption('fr');
	await expect(page.getByRole('link', { name: 'Tableau de bord' })).toBeVisible();
	await page.reload();
	await expect(page.getByRole('link', { name: 'Tableau de bord' })).toBeVisible();
	await page.getByLabel('Langue').selectOption('en');
	await expect(page.getByRole('link', { name: 'Dashboard' })).toBeVisible();
});
