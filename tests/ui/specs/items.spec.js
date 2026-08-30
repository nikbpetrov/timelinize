// Opens the item page of every item the Meta cases select and fails on any JS error.
// With ?debug=1 the page also renders the debug panel (item-debug endpoint), which is asserted.
const { test, expect } = require('@playwright/test');
const { repoID, loadItems, loadManifest, resolveWhere, watchErrors } = require('../helpers');

const manifest = loadManifest();
let repo, index = {};

test.beforeAll(async () => {
	repo = await repoID();
	for (const source of Object.keys(manifest.sources)) index[source] = await loadItems(repo, source);
});

for (const c of manifest.cases) {
	test(`item page: ${c.id}`, async ({ page }) => {
		const errors = watchErrors(page);
		let visited = 0;
		let hasVideo = false;
		for (const x of c.expect.items) {
			if (x.count === 0) continue;
			const items = resolveWhere(index[c.source], x.where);
			expect(items.length, `${c.id}: no items for ${JSON.stringify(x.where)} (is the fixture imported into ${repo}?)`).toBeGreaterThan(0);
			for (const it of items.slice(0, 3)) {
				if ((it.data_type || '').startsWith('video/')) hasVideo = true;
				await page.goto(`/items/${repo}/${it.id}?debug=1`);
				await expect(page.locator('#item-id')).toHaveText(String(it.id), { timeout: 10_000 });
				// the debug panel must render for every item
				const panel = page.locator('#item-debug');
				await expect(panel).toBeVisible({ timeout: 10_000 });
				await expect(panel.locator('[data-section="item"] pre')).toContainText(`"id": ${it.id}`);
				// the relationship graph (fork) must draw at least the item itself and its owner
				await expect(page.locator('#item-graph-svg .node.seed')).toBeVisible({ timeout: 10_000 });
				expect(await page.locator('#item-graph-svg .node').count()).toBeGreaterThan(1);
				visited++;
			}
		}
		// the item page probes every image for an embedded motion photo and logs the 404 as an
		// error ("loading video: Event undefined"); that is upstream noise, not a regression
		const real = errors().filter(e => !(e.includes('loading video: Event') && !hasVideo));
		expect(real, `${c.id}: JS errors on item page(s)`).toEqual([]);
		test.info().annotations.push({ type: 'visited', description: String(visited) });
	});
}
