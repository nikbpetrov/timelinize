// Playwright config for the Timelinize UI smoke tests (fork).
// Runs against a live server (default: the dev server on :12003 with the Meta fixture imported).
//   TLZ_BASE_URL=http://127.0.0.1:12003 npx playwright test
const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
	testDir: './specs',
	timeout: 30_000,
	retries: 0,
	workers: 2,
	reporter: [['list'], ['html', { open: 'never', outputFolder: 'report' }]],
	use: {
		baseURL: process.env.TLZ_BASE_URL || 'http://127.0.0.1:12003',
		headless: true,
		screenshot: 'only-on-failure',
		trace: 'retain-on-failure',
	},
});
