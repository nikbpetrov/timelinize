# Shared-link handling

## How upstream handles links today
Nothing is fetched anywhere; all importers are offline. In DMs, `share.link` + `share.share_text` are concatenated onto
the message text (`datasources/facebook/messages.go:142-151`), owned by the sender, no metadata, `original_content_owner`
dropped. Precedent for link items: GitHub stars -> `ClassBookmark` with URL/description in metadata; Firefox history ->
`ClassPageView`; `timeline.DownloadData(url)` exists (used for Twitter avatars) as a lazy HTTP `Content.Data`.

## URL taxonomy -> strategy
| Kind | URL shape | Strategy | Notes |
|---|---|---|---|
| IG reel | `instagram.com/reel/<code>` | yt-dlp download + info.json | works with and without cookies for public reels |
| IG post | `instagram.com/p/<code>` | yt-dlp (videos) -> gallery-dl (images) | yt-dlp fails "No video formats found" on image slides |
| IG story (other's) | `instagram.com/stories/<user>/<id>` | never fetch; metadata `{Author, Story ID, Original time (from id), Status: expired}` | stories expire in 24 h; verified unreachable |
| IG story (own) | same, own username | offline match to imported story item (id -> ms timestamp, within 2 s) -> `RelQuotes` | no network |
| FB reel/video | `facebook.com/reel/...`, `/videos/`, `/watch`, `fb.watch` | yt-dlp download + info.json | verified with cookies |
| FB post/group/permalink/page | `facebook.com/groups/...`, `permalink.php`, `<page>/posts/...` | metadata only (URL, kind, export caption) | yt-dlp returned title/text for a group post by accident; events fail to parse; don't rely on it |
| FB event | `facebook.com/events/<id>` | metadata only; later match `fbid` to native `events/` import | |
| external (youtube, podcasts, sites) | anything else | metadata only (URL, host) | |

## Trial results (2026-08-30, yt-dlp 2026.08.19)
- IG reel `DRjA8jODFUs`: full metadata (title, uploader, uploader_id, timestamp, duration, w/h, like/comment counts,
  description), 13 formats, thumbnails. Download `DRO3V-NlOXS`: 9.6 MB 1080x1920 mp4 + jpg + info.json in ~2 s.
- IG reel without cookies: also resolves (public content).
- IG `/p/CwFd36dI907/` carousel: 3 video slides downloaded, 5 image slides -> "No video formats found".
- IG story `2669473257599697153`: "content is unreachable" even with cookies.
- FB reel `546824930845320`: 4.3 MB mp4, metadata (title, uploader "Greatness", 51 s, 2023-07-20).
- FB group post: yt-dlp returned id/title/text (no media). FB event: "Cannot parse data".

## Implementation (fork, verified on dev)
- `internal/linkfetch`: `Resolver.Resolve(ctx, Request{URL, Kind, Site}) Result{Status, Backend, Files[], Error, Cached}`.
  Routing: `reel|video` -> yt-dlp; `post|photo` on Instagram -> gallery-dl (handles mixed carousels; yt-dlp fallback);
  `story` -> `expired`; everything else (FB groups/events/pages, profiles, external) -> `metadata_only`, never fetched.
- Statuses: `resolved | metadata_only | expired | unavailable | failed | unresolved` — terminal ones are never
  retried; `failed` retried up to `max_attempts` (3) on later imports; `unresolved` = not attempted (disabled or
  per-import budget exhausted), retried next import.
- Cache: `<repo>/linkfetch/<sha1[:2]>/<sha1>/{result.json, files…, *.info.json|*.json}` keyed by canonical URL;
  files stay so re-imports never hit the network (the pipeline re-reads data on every import). ~5 MB per reel.
- Config: per job `processing_options.link_fetch.{enabled, max_per_import, delay_ms, timeout_s, cookies{site:path},
  cache_dir, max_attempts, ytdlp_path, gallerydl_path}`; defaults from `config.json` `"link_fetch"` (cookies live
  there). CLI: `--job.processing_options.link_fetch.enabled true --job.processing_options.link_fetch.max_per_import 5`.
- Data model: bookmark keeps the URL as text + metadata `Status`, `Fetched with`, `Fetch error`; each downloaded
  file -> `ClassMedia` item `original_id = <url>#<slide>` attached to the bookmark (`attachment` edge) with normalized
  metadata (`Author`, `Author name`, `Description`, `Published`, `Duration`, `Width/Height`, `Likes`, `Comments`,
  `Views`, `Slide i/n`, `Location`, `Source URL`) and timestamp = publish time. Data files land in
  `data/YYYY/MM/<source>/` like any other media (and therefore go to Immich).
- Logging: logger `job.action.link_fetch` — INFO `link fetched {url, kind, backend, status, duration_ms, files, bytes,
  attempt}`, ERROR `link fetch failed {…, error: <stderr tail>}`, INFO budget/policy notices; end-of-import
  `messages: share links summary {by_kind, placeholders_dropped, own_stories_matched, link_fetch: {resolved, …,
  network_fetches}}`.
- Dev results: 5 reels resolved in 2.8–3.9 s each (3 s delay between), second import 0 network fetches for them,
  1 reel `failed` with `HTTP Error 400` from Instagram (will retry twice more). gallery-dl carousel test: 8 slides
  (3 mp4 + 5 jpg) in 8 s.

## Resolver design (original notes)
- Package `linkfetch`: `Resolver.Resolve(ctx, url) (Result{Kind, Status, Backend, Metadata, Files []File, Err})`.
- Backends: `ytdlp` (`--cookies F --write-info-json --no-warnings -o <dir>/%(id)s_%(playlist_index)s.%(ext)s`),
  `gallerydl` (same cookies file, `--write-metadata`), `none`.
- Router: by kind table above; `gallerydl` only after `ytdlp` fails with the image error.
- Rate limit: 1 worker, `delay_ms` between calls (default 3000), `timeout_s` per call (default 120), retry x2 on 429/5xx.
- Cache: `link_fetches` table keyed by canonical URL (strip `?id=`, `?igsh=`, `carousel_share_child_media_id`, utm);
  statuses `resolved | metadata_only | expired | unsupported | failed`; re-import never re-hits `resolved/expired/unsupported`;
  `failed` retried with backoff (attempts, tried_at).
- Logging: logger `link_fetch` — INFO per URL `{url, kind, backend, status, duration_ms, bytes, cached}`; ERROR with
  trimmed subprocess stderr; per-import summary `{by_kind: {resolved, metadata_only, expired, failed}}`.
- Output: bookmark item (`original_id` = canonical URL) with metadata; downloaded media -> item data (-> Immich in Phase 4);
  extra slides -> attached media items.
