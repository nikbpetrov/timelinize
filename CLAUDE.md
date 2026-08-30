# Timelinize — architecture quick reference

Timelinize imports personal data exports (photos, chats, social media, location history, contacts…)
into one local **timeline repository**: a SQLite index plus date-organized copies of media files.
Everything below is a map of *how data-source-specific input becomes the unified item/entity graph*,
with `file:line` pointers into the code. (Line numbers as of Aug 2026; grep the symbol if they drift.)

Fork: `origin` = github.com/nikbpetrov/timelinize, `upstream` = github.com/timelinize/timelinize.

## Data flow at a glance

```
 export on disk (dir or .zip, read-only)          ground-truth/<source>/
        │
        │  archives.DeepFS → timeline.DirEntry (fs.FS rooted at the export)
        ▼
 ┌─ datasources/<name> ──────────────────────────────────────────────────────┐
 │ Recognize(DirEntry) → confidence            FileImport(DirEntry, params) │
 │   "is this mine?"                             walks files, parses JSON…   │
 │                                               builds *Graph per thing     │
 │                                               params.Pipeline <- graph    │
 └────────────────────────────────────────────────────┬──────────────────────┘
                                                      │  chan *Graph
                                                      ▼
 ┌─ timeline (import pipeline, one processor per file) ──────────────────────┐
 │ beginProcessing: batch ~10 graphs                       processing.go:49  │
 │  0 sanitizeAndEnhance  fix coords/dates, tz, Timeframe skip pipeline.go:98│
 │  1 download            Content.Data() → sniff type → text│file + BLAKE3  │
 │                        small text → items.data_text                       │
 │                        else      → data/YYYY/MM/<src>/file  (FullPath)    │
 │  2 one SQL tx/batch    processGraph:                                      │
 │        item   → loadItemRow (dedup) → insert/update/skip  processing.go:844│
 │        entity → match by identifying attrs → insert/merge  entities.go:472│
 │        edge   → linkRelation → relationships row          processing.go:162│
 │  post-job: prune empty items, thumbnails job, embeddings job              │
 └───────────────────────────┬───────────────────────────────────────────────┘
                             ▼
      repo/timeline.db  ──  items · entities · attributes · entity_attributes
      repo/data/…            relationships · relations · classifications · jobs
      repo/thumbnails.db     (cache, regenerable)
                             │
                             ▼
 ┌─ tlzapp ── one endpoint table = HTTP /api/<name> = CLI `timelinize <name>` ─┐
 │ search-items / conversation / entities …   media served via tl.FullPath   │
 └───────────────────────────┬───────────────────────────────────────────────┘
                             ▼
                      frontend/ (vanilla JS)   http://host:12002
```

### Data model at a glance

```
            Graph { Item | Entity ; Edges []Relationship }
                       ▲ built by data sources, consumed by pipeline

  ┌───────────── Item ─────────────┐        ┌─────────── Entity ───────────┐
  │ ID (original_id, may be empty) │        │ Type person|creature|place   │
  │ Classification message|media|… │ Owner  │ Name, Picture                │
  │ Timestamp/Timespan/Timeframe   │───────▶│ Attributes []Attribute       │
  │ Location                       │        │   Identity   = who I am on   │
  │ Content: text | data_file      │        │               this source    │
  │ Metadata map                   │        │   Identifying= usable to     │
  │ Original/IntermediateLocation  │        │               find me        │
  └───────────────┬────────────────┘        └──────────────┬───────────────┘
                  │  Relationship{Relation{label,directed,subordinating},value}
                  │  attachment · sent · reply · reacted · quotes · in_collection · visit …
                  └────────── item↔item  or  item↔entity (via attribute row) ──┘

  identity/dedup order:  retrieval_key → data_source+original_id → hashes →
                         unique constraints (timestamp, data, filename, latlon, class) → file hash
  conversations = message items + `sent` edges (derived, not a table)
```

## Repo layout

| Path | Role |
|---|---|
| `main.go` | Entry point; blank-imports every data source package (`main.go:26-52`) so their `init()` registers them |
| `timeline/` | Core: data model, import pipeline, SQLite schema, jobs, search, entities, thumbnails, ML hooks |
| `datasources/<name>/` | One package per data source; each implements `timeline.FileImporter` |
| `tlzapp/` | App layer: HTTP API + CLI (symmetric), config, server, frontend serving, Python ML sidecar |
| `frontend/` | Vanilla JS/HTML/CSS UI, no build step (`pages/*.html`, `resources/js/*.js`) |
| `internal/` | `tlzmedia` (libvips image ops), `airports` (IATA lookup), `oauth2client`; **fork:** `linkfetch` (yt-dlp/gallery-dl resolver + cache), `immich` (API client) |
| `scripts/` | **fork:** `dev-reset.sh` / `dev-import.sh` (rebuild the dev repo from the fixture in ~1 min), `dev-counts.py`, `verify-import.py`, `build-testing-data.py`, `inventory-facebook.py` (→ `docs/fork/facebook-cases.md`, the per-case status list) |
| `testdata/meta/`, `tests/meta/`, `tests/ui/` | **fork:** case manifest, Go import harness (`go test ./tests/meta`, ~4 s), Playwright UI smoke tests — see `docs/fork/testing.md` |
| `cmd/cmd.go` | CLI-only subcommands: `serve`, `help`, `reset`, `version`; anything else is an API endpoint name |

## Dev setup (this machine)

- Toolchain: Go at `/usr/local/go/bin`; **build with `GOTOOLCHAIN=go1.25.8`** (go.mod pins 1.25.8; Go 1.27 breaks grpc).
- CGO deps: sqlite3 headers, ffmpeg, and **libvips ≥ 8.17** (built from source into `/usr/local`; Debian's 8.16 is too old for `vipsgen`).
- Build: `PKG_CONFIG_PATH=/usr/local/lib/pkgconfig GOTOOLCHAIN=go1.25.8 CGO_ENABLED=1 go build -o /usr/local/bin/timelinize .`
- Run server (UI reachable on LAN): `TLZ_ADMIN_ADDR=0.0.0.0:12002 TLZ_ORIGIN=http://10.0.10.46:12002 timelinize serve`
- CLI commands need the same `TLZ_ADMIN_ADDR` to reach the running server; `--repo` takes the repo **instance_id** (`timelinize open-repositories`), not the path.
- Data lives on the NFS mount `/mnt/photos/timelinize` (10.0.10.13, 4 TB): `ground-truth/<source>/` = untouched exports, `repo/` = the Timelinize repository, `logs/server.log`. Root disk is only 10 GB — keep data off it.
- Working CLI import (unique constraints are mandatory for ID-less sources — see "Identity & dedup"):
  ```
  timelinize import --repo <instance_id> \
    --job.plan.files[0].data_source_name instagram --job.plan.files[0].filenames[0] /mnt/photos/timelinize/ground-truth/ig \
    --job.processing_options.item_unique_constraints.filename true \
    --job.processing_options.item_unique_constraints.timestamp true \
    --job.processing_options.item_unique_constraints.latlon true \
    --job.processing_options.item_unique_constraints.classification_name true \
    --job.processing_options.item_unique_constraints.data true \
    [--job.processing_options.timeframe.since 2025-11-01T00:00:00Z]   # RFC3339 only
  ```
- Quick verification: `scripts/dev-counts.py [repo]`, `scripts/verify-import.py <source> <export> [filters]`; the import
  job's final `message` holds "N new, N updated, N skipped items; N new entities".
- Dev loop: server :12003 (`XDG_CONFIG_HOME=/root/.config/timelinize-dev`) on `repo-dev`; `scripts/dev-reset.sh` after a build imports the
  **testing fixture** (`/mnt/photos/timelinize/testing-data`, built from `testdata/meta/messages.json`; `TLZ_CASES=all` adds `posts.json`). Item pages take `?debug=1` for the raw-data panel.
- Tests: `go test ./tests/meta` (import-level, real pipeline) and `cd tests/ui && npx playwright test` (UI, needs the dev server). Add a case to
  `testdata/meta/messages.json` (or `posts.json`) for every reported problem; the harness prints actual rows/edges on failure.
- Fork features are configured in `config.json`: `link_fetch` (cookies, delays; per-job override via
  `processing_options.link_fetch`) and `immich` (url, api_key_file, album). Dev config has both; main (:12002) not yet.

## Where to look (file:line)

| Concept | Code |
|---|---|
| `Graph`, `Item`, `ItemData`, `Relationship`, `Relation`, predefined relations / classifications | `timeline/graph.go:41, :195, :494, :1048, :1089, :790-803, :1186-1199` |
| `Entity`, `Attribute` (`Identity` vs `Identifying`), canonical attr names | `timeline/entities.go:54, :206, :239-247, :1188` |
| `DataSource`, `RegisterDataSource`, `FileImporter`, `DirEntry`, `ImportParams` | `timeline/datasource.go:55, :117, :503, :223`; `timeline/imports.go:419` |
| Data source registration (blank imports) | `main.go:26-52` |
| Simple data source example (GPX) / Instagram / shared Meta messages | `datasources/gpx/gpx.go:186-215` / `datasources/instagram/instagram.go` / `datasources/facebook/messages.go` |
| Job → processor → batching | `timeline/imports.go:124`, `processor.go:31,60`, `processing.go:49` (batch size `:47`) |
| Phase 0/1/2 | `pipeline.go:98` (sanitize, Timeframe skip `:202`), `:266` (download; text-vs-file `:334`), `:596` (tx) |
| Dedup: `loadItemRow`, insert/update/skip decision, file-hash dedup | `processing.go:844`, `:334`, `pipeline.go:455` |
| Entity match/merge | `entities.go:472`, `:730-818`, `:966` |
| Relationships | `processing.go:162` → `timeline.go:588` |
| Data file naming/location, `FullPath` | `itemfiles.go:127, :175, :343` |
| Field update policies | `timeline.go:1389` (`KeepExisting < PreferExisting < OverwriteExisting < PreferIncoming`) |
| Schema (embedded) | `timeline/schema.sql` — `items:157`, `entities:74`, `attributes:101`, `entity_attributes:117`, `relationships:223`, `jobs:40`, view `extended_items:366`; thumbnails: `thumbnails.sql` |
| Jobs, states, checkpoints; in-memory-only import counters | `jobs.go:1177-1210`; `imports.go:66,126`, logged at `pipeline.go:645` |
| API+CLI endpoint table; flag→JSON | `tlzapp/endpoints.go:29`, `cli2api.go:65`, `argparse.go` |
| Media serving (the seam for any external store) | `tlzapp/frontend.go:177, :221, :347, :451` |
| Config / env | `tlzapp/config.go` (`~/.config/timelinize/config.json`, `TLZ_ADMIN_ADDR`, `TLZ_ORIGIN` → `server.go:85`) |
| Python ML sidecar (embeddings/classify) | `tlzapp/python/server/server.py`, `timeline/ml.go` |
| Demo-mode obfuscation (display copies only) | `timeline/obfuscation.go` |
| **Fork:** shares -> bookmarks, placeholders, own-story match, resolver call | `datasources/facebook/shares.go`, `messages.go` (share block) |
| **Fork:** item relationship graph (`item-graph` endpoint, force-layout SVG on the item page) | `timeline/itemgraph.go`, `frontend/resources/js/item.js` (`renderItemGraph`) |
| **Fork:** Messenger message classifier (calls, group/thread events, locations, bare URLs), per-thread anonymous identities, group threads as collections, E2EE export walker | `datasources/facebook/classify.go` (+ `_test.go`), `identity.go`, `messages.go` (`messageWalker.processThread`), `e2ee.go`; plan/status `docs/fork/messenger-plan.md`, `facebook-cases.md` |
| **Fork:** link resolver, cache, statuses | `internal/linkfetch/linkfetch.go`; options `timeline/linkfetch.go` |
| **Fork:** Immich store: options, upload job, evict, `EnsureDataFile` restore | `timeline/immich.go`, `internal/immich/client.go`, table `immich_assets` in `schema.sql` |
| **Fork:** persisted import counters, `FinalMessage`, job double-start fix | `timeline/imports.go` (`ImportCounters`), `jobs.go` (`startJob`) |

## Facts that bite

- **No IDs in Meta exports** → identity = unique constraints only; CLI imports **must** pass `item_unique_constraints` or ID-less items error out while the job still says "succeeded". Same `timestamp+text` ⇒ merged.
- Text < 100 KiB (`processing.go:1514`) lives in `items.data_text`; everything else is a file under `data/YYYY/MM/<source>/`, referenced repo-relative in `items.data_file`; duplicate bytes are detected by BLAKE3 and share one file.
- Entities are matched only through *identifying* attributes; Instagram contacts are identified by display name (`instagram_name`), so a rename = a new entity.
- Entity endpoints of relationships reference an **attribute row**, not the entity row (`_entity` pass-thru attribute if none).
- `Retrieval` keys fuse an item delivered in pieces (e.g. a message attachment stub + its file found by a later media walk). If the second half never arrives, the item stays with `data_file NULL`.
- Import counters (`new/updated/skipped`) are never persisted; `jobs.progress` counts graphs incl. skipped ones. Verify imports with SQL per `job_id`.
- The CLI runs in-process when no server is up — async jobs die with it. Keep `serve` running.
- Thumbnail generation for videos (ffmpeg/libvpx) dominates import time; leave `thumbnails` off to run it as a separate job.
- Timeframe values are RFC 3339; the skip only marks the root item (see backlog).
- **Fork:** share-only DMs are empty `message` rows with an `attachment` edge to a `bookmark` (URL = `original_id`);
  fetched media hang off the bookmark as `media` items (`original_id = <url>#<slide>`). Immich mapping is by
  `items.data_hash` (`immich_assets`), the local file stays the path in `data_file` and is a restorable cache.

## Known gaps / fork backlog

See `docs/fork/backlog.md` (DM attachments without data, `message_requests` not walked, Timeframe leak to attachments, CLI constraints, unpersisted counters, share-text flattening) and `docs/fork/immich-media-store.md`.
