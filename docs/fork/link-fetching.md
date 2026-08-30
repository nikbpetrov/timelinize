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

## Resolver design (Phase 3)
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
