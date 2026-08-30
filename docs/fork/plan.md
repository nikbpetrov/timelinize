# Fork plan — status (2026-08-30)

## Goals (as decided)
1. Meta exports (Instagram, Facebook) import completely and faithfully: all message folders, attachments with their files,
   shares represented as their own items (not as the sender's words), own stories/posts with media.
2. Shared links are enriched: reels/videos downloaded (yt-dlp), photo/carousel posts downloaded (gallery-dl),
   everything else metadata-only (URL, kind, author, caption, status). Own-story links resolve offline to the
   already-imported story items. Never scrape Facebook pages.
3. Media lives in **Immich** as the canonical store: uploaded after import with dedup, `visibility=archive`, in the single
   album "Timelinize", tagged per source, dated by item timestamp. Timelinize keeps ids and its own thumbnails; the local
   copy is a cache that can be evicted and is restored on demand. If Immich is down, imports/UI degrade gracefully.
4. Every remote interaction (link fetch, Immich) is logged per item with outcome, and summarised per job.
5. Re-imports from untouched ground-truth exports are idempotent and verifiable by counts.

## Status — all phases implemented and verified on the dev dataset (`repo-dev`)
| Phase | What | Verified by |
|---|---|---|
| 0 | `dev` branch on the fork, `.gitignore` for cookies/keys, gallery-dl installed | `git log`, `gallery-dl --version` |
| 1 | `fillItem` reads the media file directly when it exists in the archive (DM attachments, post media); media walks still cover multi-part exports; dev filters skip the walks | dev: 0 message items without data (was 12/26); FB post `3701647923444080.mp4` gets its file |
| 2 | Shares -> `ClassBookmark` (canonical URL = original_id; Kind/Author/Caption/Status metadata), placeholders dropped, share-only messages = empty message root with `attachment` edge, own-story links -> `quotes` edge to the story item | `scripts/verify-import.py`: 8 bookmarks, 8 share-only roots, 1 quote, 0 messages containing a link |
| 3 | `internal/linkfetch`: yt-dlp (reels/videos), gallery-dl (IG posts), cookies, on-disk cache, rate limit, per-import budget, retries, normalized metadata; media attached to the bookmark | dev: 5 reels fetched in ~3.5 s each, cache hits on re-import, budget leaves the rest `unresolved` |
| 4 | `internal/immich` client + `immich_assets` table + `immich` job (upload with SHA-1 pre-check, album, tag, metadata back-reference), `immich-sync`/`immich-status` endpoints, evict + restore-on-read | dev: 36 files mapped (32 uploaded, 3 trial dupes, 1 FB), album count 36, evict then HTTP GET restores byte-identical file in 0.19 s |
| 5 | Import counters persisted in the job's final message + checkpoint; `scripts/verify-import.py`; **fixed an upstream race where a scheduled job could start twice** | job messages "60 new, 0 updated, 0 skipped…"; verify script OK for IG and FB |

Details per topic: `link-fetching.md`, `immich-media-store.md`, `backlog.md`, `exports.md`; commands in `README.md`.

## Testing pipeline (2026-08-30) — see `testing.md`
`testdata/meta/{messages,posts}.json` (75 cases over posts / stories / messages / places / albums, with expectations) →
`scripts/build-testing-data.py` (mini export under `testing-data/`) → `go test ./tests/meta` (real pipeline, ~4 s) and
`scripts/dev-reset.sh` + `tests/ui` Playwright (item pages with the `?debug=1` panel, conversations). First run found 7
bugs (backlog #18–#24).

## Next
0. **Decide on reaction pseudo-messages** (#25): drop them (and optionally keep their time on the `reacted` edge).
   The cases are in place; flipping the expectations is a one-line change.
1. **Run on the main repo** (`/mnt/photos/timelinize/repo`, server :12002): add `link_fetch` + `immich` to
   `~/.config/timelinize/config.json` (same shape as the dev config), rebuild `timelinize`, then re-import both exports
   (no filters, `link_fetch.max_per_import` unset). Expect ~3,000 link fetches at 3 s delay (~3 h) — run overnight, watch
   `logs/server.log` for `link_fetch` errors (rate limits / login walls => re-export cookies). Then `immich-sync` for the
   existing media, and `--evict true` once the thumbnails job has finished.
2. Frontend: bookmark items in conversations render as empty messages with an attachment; a small card (kind, author,
   caption, fetched media) in `frontend/` would make shares readable. Not started.
3. Facebook: `events/`, `groups/`, comments importers; `data_messenger_e2e/` (different schema); own posts now exist in
   the export (390 posts, 113 with media) and import fine.
4. Owner-identity merge (#9), Timeframe-skip propagation (#3).
5. Immich reconciliation job (assets trashed/deleted in Immich; rebuild `immich_assets` from asset metadata).

## Needs from the user
- Nothing blocking. Optional: decide whether the main repo should evict local media (`immich.evict_after_upload`) or keep
  the local cache; whether `link_fetch` should run during the big re-import or be limited per import.
