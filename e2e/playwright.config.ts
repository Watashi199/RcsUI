import { defineConfig, devices } from '@playwright/test';

// Tests de bout en bout contre l'image Docker en cours d'exécution (voir run.sh).
// Les tests partagent un seul serveur : exécution séquentielle.
export default defineConfig({
	testDir: './tests',
	globalSetup: './global-setup.ts',
	fullyParallel: false,
	workers: 1,
	retries: process.env.CI ? 1 : 0,
	timeout: 60_000,
	expect: { timeout: 15_000 },
	reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
	use: {
		baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:18080',
		// Interface en anglais : les sélecteurs utilisent les libellés EN.
		locale: 'en-US',
		trace: 'retain-on-failure',
		screenshot: 'only-on-failure'
	},
	// E2E_CHANNEL=msedge|chrome : utiliser un navigateur déjà installé plutôt que celui de Playwright.
	projects: [
		{ name: 'chromium', use: { ...devices['Desktop Chrome'], locale: 'en-US', channel: process.env.E2E_CHANNEL || undefined } }
	]
});
