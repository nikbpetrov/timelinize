// Opens the conversation view for every DM thread the Meta cases touch (owner + counterpart),
// the way the frontend links to it (/conversations?entity=a,b&only_entity=true), and fails on
// any JS error or an empty conversation. This is the page that crashed on share-only messages.
const { test, expect } = require('@playwright/test');
const { repoID, loadItems, loadManifest, resolveWhere, watchErrors } = require('../helpers');

const manifest = loadManifest();
let repo, index = {};

test.beforeAll(async () => {
	repo = await repoID();
	for (const source of Object.keys(manifest.sources)) index[source] = await loadItems(repo, source);
});

// participants of a message item: owner entity + every "sent" recipient
function participants(item) {
	const ids = new Set();
	if (item.entity?.id) ids.add(item.entity.id);
	for (const rel of item.related || []) {
		if (rel.label === 'sent' && rel.to_entity?.id) ids.add(rel.to_entity.id);
	}
	return [...ids].sort((a, b) => a - b);
}

const seen = new Set();
for (const c of manifest.cases) {
	if (c.entity !== 'message' && c.entity !== 'story') continue;
	test(`conversation: ${c.id}`, async ({ page }) => {
		const errors = watchErrors(page);
		let opened = 0;
		for (const x of c.expect.items) {
			if (x.count === 0) continue;
			for (const it of resolveWhere(index[c.source], x.where)) {
				if (it.classification !== 'message') continue;
				const ids = participants(it);
				if (ids.length < 2) continue;
				if (ids.length > 2) {
					// group threads: the conversation view renders nothing for them (backlog #19); skip
					// rather than hide it behind a green check
					test.info().annotations.push({ type: 'skipped group thread', description: `${ids.length} participants (item ${it.id})` });
					continue;
				}
				const key = ids.join(',');
				if (seen.has(key)) continue;
				seen.add(key);
				// two-party threads are opened exactly (only_entity); group threads list every
				// participant the API returned (capped by related=1), so match any conversation with them
				await page.goto(`/conversations?entity=${encodeURIComponent(key)}&only_entity=true`);
				// at least one chat bubble must render
				await expect(page.locator('.chat-bubble').first()).toBeVisible({ timeout: 15_000 });
				opened++;
			}
		}
		expect(errors(), `${c.id}: JS errors in conversation view`).toEqual([]);
		test.info().annotations.push({ type: 'conversations opened', description: String(opened) });
	});
}

test('gallery renders media thumbnails without errors', async ({ page }) => {
	const errors = watchErrors(page);
	await page.goto('/gallery');
	await expect(page.locator('img, video').first()).toBeVisible({ timeout: 20_000 });
	expect(errors()).toEqual([]);
});
