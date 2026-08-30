# Fork plan — current form (2026-08-30)

## Goals (as decided)
1. Meta exports (Instagram, Facebook) import completely and faithfully: all message folders, attachments with their files,
   shares represented as their own items (not as the sender's words), own stories/posts with media.
2. Shared links are enriched: reels/videos downloaded (yt-dlp), photo posts downloaded (gallery-dl fallback),
   everything else metadata-only (URL, kind, author, caption, status). Own-story links resolve offline to the
   already-imported story items. Never scrape Facebook pages.
3. Media lives in **Immich** as the canonical store: uploaded at import with dedup, `visibility=archive`, in the single
   album "Timelinize", tagged per source, dated by item timestamp. Timelinize keeps ids/metadata and its own thumbnails;
   if Immich is down, imports/UI degrade gracefully. Local cache is rebuildable from Immich (and vice versa).
4. Every remote interaction (link fetch, Immich) is logged per item with outcome, and summarised per import.
5. Periodic re-imports from untouched ground-truth exports are idempotent and verifiable by counts.

## Status
- Done on `dev` branch (uncommitted): dev filters (`facebook.Filters`), extra message folders (`message_requests`,
  `filtered_threads`, `e2ee_cutover`), FB username fallback, reaction-actor `FixString`, docs.
- Trials done: yt-dlp + cookies (reels OK, photo posts need gallery-dl), Immich API (upload/dedup/fetch/album OK;
  needs `asset.update`).

## Phase 0 — housekeeping (next)
- Commit current work on `dev`; `.gitignore` for cookies/scratch; push to fork.

## Phase 1 — attachments get their files (backlog #1)
- Instagram: walk `your_instagram_activity/messages/**/{photos,videos,audio,gifs,files}` and emit retrieval-keyed media
  items (mirror `facebook.processPostMedia`), so `fillItem` stubs get fused with real files. Same walk for FB
  `e2ee_cutover` / `message_requests` / `filtered_threads`.
- Done when: dev repo has 0 message items with `data_file NULL`; full IG repo 124/124.

## Phase 2 — shares become bookmark items (backlog #7)
- `messages.go`: message keeps only typed text; Meta placeholders ("You sent an attachment.", localized variants) dropped;
  if nothing remains, the share/attachment becomes the graph root (existing pattern).
- Attached `ClassBookmark` item per share: `original_id` = canonical URL (dedupes across exports even when captions
  change), `data_text` = URL, metadata `{URL, Kind, Author, Caption, Original content owner, Resolve status}`,
  owner = author entity (`instagram_username` attribute) when known, else sender; edge `RelAttachment`.
- Own-story links (`/stories/<own username>/<id>`): story id -> ms timestamp; match imported story item within 2 s -> `RelQuotes`.
- Done when: the 7386-style message in dev shows caption as bookmark metadata; per-kind counts logged.

## Phase 3 — link resolver + downloads (new package)
- `linkfetch` package: `Resolver` interface, backends `ytdlp`, `gallerydl`, `none`; router by URL kind
  (see `link-fetching.md`). Subprocess with `--cookies`, `--write-info-json`, timeout; 1 worker, configurable delay; retry on 429.
- Table `link_fetches(url PK, kind, status, backend, tried_at, attempts, error, info_json, files)` — cache across imports.
- Fetched file -> bookmark `Content.Data`; extra carousel slides -> `RelAttachment` media items; info.json -> metadata.
- Config `link_fetch: {enabled, cookies: {instagram, facebook}, delay_ms, max_per_import, timeout_s}`.
- Logging: logger `link_fetch`; INFO per URL `{url, kind, backend, status, duration, bytes, error}`; DEBUG stderr on
  failure; end-of-import summary by kind; failures persisted.
- Install `gallery-dl`. Done when dev repo resolves: reel -> mp4, `/p/` carousel -> N images, story -> expired, FB text -> metadata.

## Phase 4 — Immich media store (see `immich-media-store.md`)
- `ItemDataStore` seam (`Put/Open/Thumbnail/Delete/Exists`) with `local` backend = today's behaviour (pure refactor).
- `immich` backend for `image/*`, `video/*`: SHA-1 pre-check -> upload (`fileCreatedAt` = item timestamp, `filename`
  `tlz-<source>-<name>`, `visibility=archive`, `metadata=[{timelinize:{repo,item_id,data_source,data_hash}}]`) -> add
  to album "Timelinize" -> tag `timelinize/<source>`; `items.data_file = immich://<asset id>`; keep BLAKE3 `data_hash`.
- Serving: proxy `/original`, `/thumbnail?size=`, `/video/playback` (Range passthrough); local thumbnail cache kept.
- Degradation: Immich unreachable -> fall back to local store for that item and log; reconciliation job later.
- Migration job for existing local media (idempotent via `bulk-upload-check`).
- Logging: logger `immich`; per asset `{item_id, asset_id, status created|duplicate|failed, bytes, duration, error}`; summary per import.

## Phase 5 — verification & counters (backlog #4, #5)
- Persist `new/updated/skipped/total` on the jobs row; CLI import defaults unique constraints (or fails loudly).
- `scripts/verify-import.py <source> <export> <repo>`: expected vs actual per classification, attachments with data,
  shares resolved, Immich assets present.

## Parked
- Native importers for Facebook `events/`, `groups/`, comments; `data_messenger_e2e/` format (different JSON schema).
- Owner-identity merge (#9); Timeframe-skip propagation (#3); own FB posts (need an export that contains posts).

## Needs from the user
- Immich key: add `asset.update` (dates, archive visibility, metadata updates) and `tag.asset`; keep `asset.delete` off.
- A Facebook export that includes Posts (current one has none).
- Check Immich Admin -> Jobs: thumbnails for uploaded assets never generated during the trial.
