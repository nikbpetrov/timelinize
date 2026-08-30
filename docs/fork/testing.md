# Testing pipeline for the Meta importers

Three layers, one set of manifests. Everything hangs off `testdata/meta/*.json`: lists of **cases** (entity ×
subtype, each pointing at real records in the ground-truth exports) with the representation we expect after import.
`messages.json` (Messenger + Instagram DMs) is the active set; `posts.json` (posts, stories, albums, places) is parked.
`TLZ_CASES` selects manifests everywhere (`messages` default, `posts`, `all`, or a comma list) — build the fixture and
run the tests with the same value.

```
 ground-truth exports ──build-testing-data.py──▶ testing-data/ (mini export, real layout, 64 MB)
                                                     │
                 ┌───────────────────────────────────┼──────────────────────────────┐
                 ▼                                   ▼                              ▼
   go test ./tests/meta                 scripts/dev-reset.sh                 tests/ui (Playwright)
   real pipeline → temp repo            fixture → dev server :12003           opens every case's item page (+debug
   asserts each case via SQL (~4 s)     for looking at things in the UI       panel) and conversation; fails on JS errors
```

| Piece | Where | Run |
|---|---|---|
| Case manifests | `testdata/meta/messages.json` (46 cases, 7 checks, active), `posts.json` (29 cases, parked) | `TLZ_CASES=messages|posts|all` |
| Fixture builder | `scripts/build-testing-data.py` → `/mnt/photos/timelinize/testing-data/{instagram,facebook/data,facebook/data_messenger_e2e}` + `MANIFEST.md` | `python3 scripts/build-testing-data.py` (`TLZ_CASES=all` for everything) |
| Import-level tests | `tests/meta/meta_test.go` (vocabulary in `tests/meta/README.md`) | `PKG_CONFIG_PATH=/usr/local/lib/pkgconfig GOTOOLCHAIN=go1.25.8 CGO_ENABLED=1 go test ./tests/meta` |
| Same checks on a live repo | — | `TLZ_TEST_REPO=/mnt/photos/timelinize/repo-dev go test ./tests/meta` |
| Dev server with the fixture | `scripts/dev-reset.sh` (wipes `repo-dev`, imports the fixture; Immich on, link fetching **off** unless `LF_ENABLED=true`) | ~1 min |
| UI smoke tests | `tests/ui/specs/{items,conversations}.spec.js` | `cd tests/ui && TLZ_BASE_URL=http://127.0.0.1:12003 npx playwright test` (~3 min; `npx playwright show-report`) |
| Item debug panel | `/items/<repo>/<id>?debug=1` (sticky per browser; `?debug=0` turns it off); API `item-debug --repo --item_id` | — |

The fixture is personal data and lives outside the repo (next to `ground-truth/`); the manifest in the repo only
contains selectors (thread names, timestamps) and short expected snippets.

## Workflow when something looks wrong
1. Open the item with `?debug=1`: raw row, every edge (both directions) with entity names, owner chain, data file on
   disk (path, size, BLAKE3 vs DB), Immich asset (id, web/API/original URLs, evicted?), link-fetch cache entry, job.
2. Find the record in the export, add a case (or extend one) with the expected representation.
3. `build-testing-data.py` → `go test ./tests/meta` shows the gap; fix; `dev-reset.sh` + Playwright confirm the UI.

## Taxonomy (what exists in these exports, and what we do with it)
Counts are from the full exports (IG 7,894 messages / 28 posts / 356 stories; FB ~1.05 M messages / 390 posts).
Case ids in `messages.json` / `posts.json`; ✔ = represented as intended, ⚠ = current behaviour documented as `known_issue`.

### Instagram — posts (own)
| Subtype | # | Case | Representation |
|---|---|---|---|
| carousel with caption | 12 | `ig-post-carousel-caption` | ✔ text = social root; each media = attached social item |
| single image, caption on `media[0].title` | 12 | `ig-post-single-title-on-media` | ✔ |
| single video | 4 | `ig-post-single-video` | ✔ |
| mixed jpg+mp4 carousel | 1 | `ig-post-mixed-carousel` | ✔ (slides keep their own `creation_timestamp`) |

### Instagram — stories (own)
| jpg / mp4 / webp × caption / none | 356 | `ig-story-image-caption`, `-video-nocaption`, `-webp` | ✔ media item owned by you, Caption in metadata |
| story linked from a DM | 266 links | `ig-story-quoted-from-dm`, `-quoted-with-text`, `ig-msg-message-requests` | ✔ `quotes` edge to the one story item (retrieval key prevents the duplicate that concurrent batches produced) |

### Instagram — messages
| Subtype | # | Case | Representation |
|---|---|---|---|
| plain text (Cyrillic, mojibake in raw JSON) | 1,707 | `ig-msg-text-cyrillic` | ✔ decoded |
| text with `reactions[]` | 1,792 | `ig-msg-text-with-reaction` | ✔ `reacted(emoji)` edge from the actor |
| **reaction pseudo-message** "Reacted 😂 to your message" | 702 | `ig-msg-pseudo-reaction` | ⚠ imported as a message; duplicates the edge on your message |
| "Liked a message" | 329 | `ig-msg-pseudo-like` | ⚠ same |
| photos (1 / 6 in one message) | 86 | `ig-msg-photo-single`, `-photos-multi` | ✔ first photo is the message, rest attached; files read from the archive |
| video | 9 | `ig-msg-video` | ✔ |
| voice notes (.aac, audio-in-.mp4) | 18 | `ig-msg-audio` | ✔ typed `audio/mp4` via ffprobe (were sniffed as video → broken thumbnails, Immich 400) |
| share: reel (+ "You sent an attachment." placeholder) | 2,309 | `ig-msg-share-reel` | ✔ empty message + `bookmark` (Kind, Author, Caption, Status); placeholder dropped |
| share: /p/ post | 355 | `ig-msg-share-post` | ✔ bookmark Kind=post (gallery-dl when fetching) |
| share: someone else's story | 43 | `ig-msg-share-others-story` | ✔ bookmark Kind=story, Status=expired (never fetched) |
| share: profile | 14 (+typed links) | `ig-msg-share-profile-link` | ✔ Kind=profile |
| share: Giphy GIF (arrives as a link, not `gifs[]`) | 7 | `ig-msg-share-giphy` | ✔ Kind=external |
| share: external (ChatGPT, YouTube, Facebook video…) | ~40 | `ig-msg-share-external`, `-share-facebook-video` | ✔ |
| call record (`call_duration`) | 1 | `ig-msg-call` | ⚠ text only, duration ignored |
| dropped pin (Maps link) | 1 | `ig-msg-dropped-pin` | ⚠ no coordinates |
| business auto-reply with URL in text | — | `ig-msg-typed-link-autoreply` | ✔ text kept |
| `message_requests/` threads | 190 | `ig-msg-message-requests` | ✔ walked (upstream skipped the folder) |
| group threads | 150 msgs | — | see backlog #19 (conversation view) |

### Facebook — posts (own)
| Subtype | # | Case | Representation |
|---|---|---|---|
| status text | 57 | `fb-post-status-text` | ✔ Title in metadata |
| shared link, with / without text | 76 / 22 | `fb-post-shared-link-with-text`, `-no-text` | ✔ `bookmark` attachment keyed by URL (upstream: nameless `location` item) |
| photo / video / 2 photos / 12 photos + video | 16 / 35 / 3 / 1 | `fb-post-photo`, `-video`, `-multi-photo-no-text`, `-many-photos-and-video` | ✔ media read directly; walk also covers multi-part exports |
| media + place / place only ("travelling to") | 19 / 2 | `fb-post-video-with-place`, `-place-only` | ✔ `attachment` edge to a place entity (mojibake fixed) |
| life event (description + photos / place / start+end exported as two entries) | 7 | `fb-post-life-event`, `-life-event-place`, `-life-event-end-date` | ✔ social item placed at `backdated_timestamp` ("Posted" = when added), text = title + description, photos attached, place entity; was pruned entirely (its photos then surfaced as undated orphan `media` via the media walk) |
| event share / "was attending" | 3 | `fb-post-event-share`, `-event-attended` | ✔ `event` item attached to the post, Timestamp/Timespan = event start/end; was pruned |
| wrote on someone's **profile** | 34 | `fb-post-wrote-on-timeline` | ✔ `sent` edge (upstream regex only knew "timeline") |
| cross-posted from Instagram | 9 | `fb-post-shared-from-instagram` | ✔ |
| featured-section item | 11 | `fb-post-featured-item` | ✔ |
| "posted something via <app>" (nothing else) | 18 | `fb-post-empty` | ✔ pruned (upstream pruning was a no-op: `NOT IN` with NULLs) |
| shared a reel (empty `external_context.url`) | 14 | `fb-post-shared-reel` | ⚠ nothing but the title in the export → pruned |
| tagged place / album photo / uncategorized photo | 48 / 14 albums / 364 | `fb-tagged-place`, `fb-album-photo`, `fb-uncategorized-photo` | ✔ |

### Facebook — messages
| Subtype | # | Case | Representation |
|---|---|---|---|
| text, text + reaction | 678 k / 62 k | `fb-msg-text-with-reaction`, `fb-msg-e2ee-thread`, `-archived-thread`, `-filtered-thread` | ✔ all five folders walked |
| sticker (`messages/stickers_used/`) | 11 k | `fb-msg-sticker` | ✔ |
| photo / video / gif / audio | 40 k / 1.7 k / 1.3 k / 1 k | `fb-msg-photo`, `-photo-e2ee`, `-video`, `-gif`, `-audio` | ✔ |
| `files[]` (pdf…) | 549 | `fb-msg-file` | ⚠ not imported at all |
| unsent | 1,397 | `fb-msg-unsent` | ✔ nothing imported |
| call / missed call | 2 k / 284 | `fb-msg-call`, `-missed-call` | ⚠ text only |
| `ip` field on a message | 13 k | `fb-msg-ip-key` | ⚠ ignored (empty message → nothing) |
| `is_taken_down` | 8 | `fb-msg-taken-down` | ✔ nothing imported |
| pseudo-reaction | 28 | `fb-msg-pseudo-reaction` | ⚠ as on Instagram |
| share: external (YouTube 812, 9gag 615, …), Instagram reel, FB reel, group post, event, `photo.php?fbid=`, raw fbcdn image URL | ~4 k | `fb-msg-share-*` | ✔ bookmarks; FB pages/groups/events never fetched (`metadata_only`) |
| "sent a location." (Bing maps link) | — | `fb-msg-location-share` | ⚠ no coordinates |

## Bugs this pipeline found on its first run
- `timeline` panicked when used without the app (nil obfuscation func) — the Go harness would not even start.
- The pipeline's empty-item pruning never deleted anything (`NOT IN` over a subquery containing NULLs).
- Facebook renamed "timeline" to "profile" in post titles; recipients of "wrote on X's profile" were lost.
- A story linked from a DM produced a duplicate story item when the two graphs landed in concurrent batches.
- Voice notes were typed `video/mp4` → thumbnail job errors (HTTP 500 on the item page) and Immich rejections.
- Nameless shared links in posts were empty `location` items (invisible once pruning works) → now bookmarks.
- Group-thread conversations render empty in the conversation view (backlog #19; the UI test skips them explicitly).

## Not covered yet
`data_messenger_e2e/` export format; Facebook `events/`, `groups/`, comments; Instagram group-thread events
("X added Y"); stories that are replies to other people's stories; posts with tagged people.
