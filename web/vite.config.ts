/// <reference types="vitest/config" />
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// SvelteKit 3 : la configuration est passée au plugin (svelte.config.js n'est plus supporté).
// SPA pure : la page de repli 200.html est servie par le binaire Go pour toute route inconnue.
export default defineConfig({
	plugins: [
		sveltekit({
			adapter: adapter({ pages: 'build', assets: 'build', fallback: '200.html', precompress: false })
		})
	],
	server: {
		// Développement : l'API tourne sur le binaire Go (make dev).
		proxy: { '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false } }
	},
	test: {
		include: ['src/**/*.test.ts'],
		environment: 'node'
	}
});
