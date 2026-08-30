# Immich as the media store for Timelinize

Status: **implemented and verified on the dev repo** (2026-08-30). Server: Immich **v3.1.0**.

## Implementation (fork)
- `internal/immich/client.go` — thin client: version, permissions, `bulk-upload-check`, multipart upload (with
  `x-immich-checksum`, `visibility`, custom `metadata`), get/update asset, original download, album/tag upsert+membership.
- `timeline/immich.go` — `ImmichOptions` (config), `tl.SetImmich`, the `immich` **job** (`JobTypeImmich`), `EnsureDataFile`
  (restore on read), `ImmichStatus`, `CreateImmichJob`. Schema: table `immich_assets` (`timeline/schema.sql`).
- App: `config.json` `"immich": {"enabled", "url", "api_key_file"|"api_key", "album", "tag_prefix", "visibility",
  "upload_after_import" (default true), "evict_after_upload" (default false)}`; applied to every opened repo
  (`tlzapp/bindings.go` openRepository). Endpoints/CLI: `immich-status --repo <id>`,
  `immich-sync --repo <id> [--import_job_id N] [--evict true]`.
- Flow: after each import a child job uploads that import's image/video files (distinct by `data_hash`): SHA-1 ->
  `bulk-upload-check` -> upload (`filename tlz-<source>-<name>`, `fileCreatedAt` = item timestamp, `visibility=archive`,
  metadata `timelinize{repo,item_id,data_source,data_file,data_hash}`) -> album "Timelinize" -> tag
  `timelinize/<source>` -> row in `immich_assets`. `--evict` deletes the local copy only after `GET /assets/{id}` confirms
  the asset is not trashed/offline; `evicted` is recorded.
- Read path: `tl.EnsureDataFile(ctx, data_file)` is called before every local open (data serving, transcode,
  motion photo, download, thumbnails, embeddings, dedup integrity check). Missing file + mapped asset => download
  `/original` to the same path, verify BLAKE3 against `items.data_hash`, clear `evicted`. `immich://` refs were not
  needed: `items.data_file` keeps the local path, the table maps `data_hash -> asset_id`.
- Logging: logger `job.action.immich` per asset `{item_id, data_file, asset_id, status, bytes, duration}`, summary
  `{outcomes: {created, duplicate, already_uploaded, evicted, error…}, bytes_uploaded}`; restores under `immich`.
- Verified on dev (36 files): upload 4.8 s total, `immich-status` = 36/36 mapped, album count 36 after fixing the 3
  trial assets (`visibility=archive`, correct dates via `PUT /api/assets/{id}`), evict all -> `GET /repo/<id>/data/...`
  restores the file byte-identical in 0.19 s.
- Behaviours worth knowing: (1) **duplicates keep their visibility/dates** — if the same bytes already exist in the
  user's real timeline, we must not archive or re-date them; only assets we create are archived. (2) A thumbnails job
  running concurrently with `--evict` restores files as it reads them (local = cache, so harmless); run evict after
  thumbnails finish. (3) Immich never generated thumbnails for our uploads in the trial — irrelevant, we keep our own.

## Goal
Photos and videos are stored **once**, in Immich. Timelinize keeps the index (items, entities, relationships,
timestamps, provenance) and fetches originals/thumbnails from Immich on demand. Immich stays the tool for photo
features (face/CLIP search, albums, duplicates, sharing); Timelinize does not replicate them.

## Decisions
- **External library: rejected.** External libraries dedupe by file path (per library), not by hash, and invert ownership.
- **Hybrid routing.** Only `image/*` and `video/*` go to Immich. Audio (voice notes), stickers, documents, text stay local.
  Immich rejects audio (`unsupported-format`).
- **Visibility = `archive`**, not `hidden`. Trial showed `hidden` assets are invisible everywhere in the UI (timeline,
  albums, search) — it's the state used for live-photo video halves. `archive` keeps them out of the timeline but
  visible in the Archive view and inside the album.
- **One album "Timelinize"** (id `7df17222-52a9-426d-a5ad-683eaf2a571a`, created in the trial). Optional tags
  `timelinize/<source>` (tag `timelinize/instagram` = `95f5372c-b16a-4da9-bbe7-b1316841d949` exists).
- **Naming**: upload `filename` = `tlz-<source>-<original name>` so assets are greppable outside the album.
- **Dates**: `fileCreatedAt`/`fileModifiedAt` = the **item timestamp** (Immich derives `dateTimeOriginal`/`localDateTime`
  from it when the file has no EXIF, which is most social-media media). Never the file mtime.
- **Recovery**: local mapping (`items.data_file = immich://<asset id>`, BLAKE3 `data_hash`, Immich SHA-1) is a cache;
  Immich carries `metadata=[{key:"timelinize", value:{repo, item_id, data_source, data_hash}}]` per asset so the mapping
  can be rebuilt from either side. Don't over-engineer beyond that.
- **Deletion**: Timelinize never deletes in Immich (`asset.delete` not granted). Immich is authoritative.
- **Thumbnails**: Timelinize keeps generating its own (it has the bytes at import). Immich thumbnails are optional —
  in the trial they never appeared (see Open items).

## Connection
- `https://immich.home` is behind Caddy with an internal CA (`Caddy Local Authority - ECC Intermediate`, 12-hour
  leaf certs) -> not trusted by Go/curl here. Use **`http://10.0.10.32:2283`** directly (LAN only); port 2388 is closed.
- API key: `/root/.config/timelinize/immich.key` (mode 600), key name "timelinize". Header `x-api-key`.
- Permissions granted: `asset.read/upload/view/download`, `album.create/read`, `albumAsset.create`, `tag.create/read`.
  **Still needed**: `asset.update` (dates, visibility, description, metadata updates), `tag.asset` (attach tags).
  Not wanted: `asset.delete`. Optional: `job.read` (detect stalled thumbnail queue).

## Where media touches Timelinize (the seam)
Write path — one place: `timeline/pipeline.go:266` `downloadItemData` -> `openUniqueCanonicalItemDataFile`
(`itemfiles.go:50`) -> `downloadDataFile` (BLAKE3 while streaming). Path stored repo-relative in `items.data_file`.

Read path — everything goes through `tl.FullPath(data_file)` (`itemfiles.go:343`):
- UI serving: `tlzapp/frontend.go:177` (image), `:221` (video transcode via ffmpeg), `:451/:456` (motion photo)
- Thumbnails: `tl.Thumbnail` (`frontend.go:347`), cache in `thumbnails.db` keyed by `data_file`
- Integrity check: `processing.go:306`; duplicate-file repair: `pipeline.go:455`; orphan deletion: `timeline.go:1062`
- Obfuscation fakes paths for demo mode: `obfuscation.go:513-526`

`items.data_file` is a plain text column -> `immich://<asset-uuid>` needs no schema change.

## Architecture
```go
type ItemDataStore interface {
    Put(ctx, it *Item, r io.Reader) (ref string, size int64, hashes Hashes, err error)
    Open(ctx, ref string) (io.ReadSeekCloser, error)          // original bytes; Range-capable
    Thumbnail(ctx, ref string, size ThumbSize) (io.ReadCloser, string /*mime*/, error)
    Exists(ctx, ref string) (bool, error)                      // for reconciliation
}
```
Backends: `local` (today's behaviour, byte-for-byte identical — pure refactor, first PR) and `immich`.
A router picks the backend per item by media type (+ config). Thumbnail cache stays local.
`immich.Put` = tee stream to BLAKE3 + SHA-1 -> `bulk-upload-check` -> `POST /assets` -> album add -> tag.
If Immich is unreachable: fall back to `local` for that item, log at WARN, mark for later migration.

## Verified API behaviour (trial 2026-08-30)
| Need | Endpoint | Result |
|---|---|---|
| Version | `GET /api/server/version` | 3.1.0 |
| Key introspection | `GET /api/api-keys/me` | no extra perms needed; lists granted permissions |
| Exists? | `POST /api/assets/bulk-upload-check` `{assets:[{id, checksum:<sha1 hex>}]}` | `accept` if unknown; `reject / duplicate / assetId / isTrashed` if present |
| Upload | `POST /api/assets` multipart `assetData, fileCreatedAt, fileModifiedAt, filename, visibility, duration(ms), metadata=[{key,value}]` + header `x-immich-checksum` | 201 `{id, status:"created"}`; `visibility` and custom `metadata` accepted at upload with only `asset.upload` |
| Re-upload same bytes | same | 200 `{status:"duplicate", id:<existing>}` |
| Read | `GET /api/assets/{id}` | `checksum` (base64 SHA-1), `originalFileName`, `originalPath`, `visibility`, `exifInfo` (w/h, size, dateTimeOriginal) |
| Custom metadata | `GET /api/assets/{id}/metadata` | round-trips what upload set |
| Original | `GET /api/assets/{id}/original` | 200, **byte-identical** for JPEG and MP4 |
| Video | `GET /api/assets/{id}/video/playback` with `Range` | 206 partial, `video/mp4`; `duration` field honoured |
| Search | `POST /api/search/metadata {checksum | albumIds | originalFileName, visibility, type}` | hidden asset: `visibility=timeline` -> 0, `hidden` -> 1 |
| Album | `POST /api/albums {albumName, description, assetIds}`; `PUT /api/albums/{id}/assets {ids}` | idempotent (`success:false, error:"duplicate"` per present id) |
| Tags | `PUT /api/tags {tags:["a/b"]}` | hierarchical upsert; attach needs `tag.asset` |
| Thumbnails | `GET /api/assets/{id}/thumbnail?size=thumbnail|preview` | **404 "Asset media not found"** for all trial assets, `thumbhash` null after >5 min; `size=fullsize` -> 302 to original |
| Update / delete | `PUT /api/assets/{id}`, `PUT /api/assets`, `DELETE /api/assets` | 403 without `asset.update` / `asset.delete` |

## Trial assets (fixed 2026-08-30: now `archive`, correct dates; mapped as "duplicate" by the dev import)
| asset | file | date sent in trial (wrong) | correct item timestamp |
|---|---|---|---|
| `eced74ac-3119-43d3-8e5a-80aca813e775` | `tlz-test-17921339444066458.jpg` | 2026-08-29 (file mtime) | 2021-11-21T16:42:11Z (dev item 28) |
| `a45d9760-815c-4750-ab3a-8f1f67d86cdb` | `17916742586314897.jpg` | 2021-11-05 (test value) | 2022-01-03T18:42:35Z (dev item 17) |
| `25873a38-e34f-4e61-86d8-ed021beefec3` | `18071323396892038.mp4` | 2021-12-01 (test value) | 2025-08-21T03:58:21Z (dev item 30) |

Once `asset.update` is granted: `PUT /api/assets {ids:[...], visibility:"archive"}` and per-asset `dateTimeOriginal`
fixes; verify the album then shows 3 assets.

## Pitfalls / open items
1. **Thumbnail jobs did not run** for uploaded assets in the trial — check Immich Admin -> Jobs (paused queue?).
   Independent of us, but the integration must not depend on Immich thumbnails.
2. **Availability coupling** — imports need Immich reachable; UI media breaks if Immich is down. Mitigation: local
   thumbnail cache, per-item local fallback + later migration, clear UI error.
3. **Lifecycle** — Immich trash is 30 days then gone; user deletions in Immich => Timelinize 404s (`isTrashed`/`isOffline`).
   Two items (two provenances) may share one asset => never delete Immich-side; reconciliation job later.
4. **API churn** — v3.0 was breaking; v3.2-rc deprecates flat search DTOs. Depend only on upload, fetch-by-id,
   bulk-upload-check, album add; pin a major; keep the client thin.
5. **Metadata direction** — Timelinize extracts EXIF at import (unchanged). Corrections flow Timelinize -> Immich via
   `PUT /assets` only; never the reverse automatically.
6. **Live/motion photos** — Timelinize `RelMotionPhoto` (embedded video) vs Immich `livePhotoVideoId` (separate asset).
   Needs explicit mapping; may require splitting the embed.
7. **Storage template** moves originals on disk; ids/checksums stable — irrelevant when keyed by id.
8. **Rate limits** — none documented; keep upload concurrency <= 4.
9. **Multi-repo** — one Immich key/user per Timelinize repo.
10. **Migration** of existing local media: idempotent job via `bulk-upload-check`, rewriting `data_file` refs.

## References
- Spec: https://github.com/immich-app/immich/blob/main/open-api/immich-openapi-specs.json
- Docs: https://docs.immich.app/features/libraries/ , https://docs.immich.app/features/command-line-interface/ ,
  https://docs.immich.app/administration/storage-template/ , https://docs.immich.app/administration/system-integrity/ ,
  https://immich.app/blog/v3-migration
