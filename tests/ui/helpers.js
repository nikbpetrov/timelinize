// Shared helpers for the UI smoke tests: talk to the Timelinize API the same way the
// frontend does, and load the Meta case manifest so every case's items get visited.
const fs = require('fs');
const path = require('path');

const BASE = process.env.TLZ_BASE_URL || 'http://127.0.0.1:12003';
const MANIFEST_DIR = path.join(__dirname, '..', '..', 'testdata', 'meta');

// Merge the case manifests named by TLZ_CASES ('messages' by default, 'posts', 'all' or a comma list),
// the same rule as scripts/build-testing-data.py and tests/meta.
function loadManifest() {
	let names = process.env.TLZ_CASES || 'messages';
	if (names === 'all') names = fs.readdirSync(MANIFEST_DIR).filter(f => f.endsWith('.json')).map(f => f.slice(0, -5)).sort().join(',');
	const merged = { sources: null, cases: [], checks: [] };
	const seen = new Set();
	for (const name of names.split(',').map(s => s.trim()).filter(Boolean)) {
		const m = JSON.parse(fs.readFileSync(path.join(MANIFEST_DIR, name + '.json'), 'utf8'));
		merged.sources = merged.sources || m.sources;
		for (const c of m.cases || []) {
			if (seen.has(c.id)) throw new Error(`duplicate case id ${c.id} in ${name}.json`);
			seen.add(c.id); merged.cases.push(c);
		}
	}
	return merged;
}

async function api(method, endpoint, body) {
	const res = await fetch(`${BASE}/api/${endpoint}`, {
		method,
		headers: { 'Content-Type': 'application/json' },
		body: body ? JSON.stringify(body) : undefined,
	});
	if (!res.ok) throw new Error(`${method} ${endpoint}: HTTP ${res.status} ${await res.text()}`);
	const text = await res.text();
	return text ? JSON.parse(text) : null;
}

async function repoID() {
	const repos = await api('GET', 'open-repositories');
	if (!repos.length) throw new Error('no open repository on ' + BASE);
	return repos[0].instance_id;
}

// All items (flat, with 1 degree of relationships) per data source, indexed by timestamp (ms)
// and by data_file basename, so cases can be resolved to row IDs the way tests/meta does.
async function loadItems(repo, source) {
	const res = await api('POST', 'search-items', { repo, data_source: [source], flat: true, related: 1, limit: 5000 });
	const items = res.items || [];
	const byTS = new Map();
	const byFile = new Map();
	for (const it of items) {
		const ts = it.timestamp ? Date.parse(it.timestamp) : null;
		if (ts !== null) {
			if (!byTS.has(ts)) byTS.set(ts, []);
			byTS.get(ts).push(it);
		}
		if (it.data_file) byFile.set(it.data_file.split('/').pop(), it);
	}
	return { items, byTS, byFile };
}

// Resolve one manifest "where" to items (subset of the Go harness: ts, classification, data_file suffix).
function resolveWhere(index, where) {
	let cands = where.ts != null ? (index.byTS.get(where.ts) || []) : index.items;
	if (where.classification) cands = cands.filter(i => i.classification === where.classification);
	if (where.data_file) cands = cands.filter(i => i.data_file && i.data_file.endsWith(where.data_file));
	if (where.data_file_contains) cands = cands.filter(i => i.data_file && i.data_file.includes(where.data_file_contains));
	if (where.data_text_contains) cands = cands.filter(i => (i.data_text || '').includes(where.data_text_contains));
	if (where.data_type_prefix) cands = cands.filter(i => (i.data_type || '').startsWith(where.data_type_prefix));
	if (where.has_text != null) cands = cands.filter(i => Boolean(i.data_text) === where.has_text);
	if (where.has_file != null) cands = cands.filter(i => Boolean(i.data_file) === where.has_file);
	if (where.metadata) cands = cands.filter(i => Object.entries(where.metadata).every(([k, v]) => i.metadata && String(i.metadata[k]) === String(v)));
	return cands;
}

// Collect page errors and console errors on a page; returns a function that returns them.
function watchErrors(page) {
	const errors = [];
	page.on('pageerror', err => errors.push('pageerror: ' + err.message));
	page.on('console', msg => {
		if (msg.type() === 'error') {
			const t = msg.text();
			// 404s for optional assets (e.g. thumbnails not generated yet) are noise, not bugs
			if (/Failed to load resource.*(404|net::)/.test(t)) return;
			errors.push('console.error: ' + t);
		}
	});
	return () => errors;
}

module.exports = { BASE, api, repoID, loadItems, loadManifest, resolveWhere, watchErrors };
