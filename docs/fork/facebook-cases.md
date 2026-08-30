# Facebook export — case catalogue

Every distinct kind of record in a Facebook "Download your information" export (JSON), what we do with it today,
and what is left. Derived mechanically from the full export with `scripts/inventory-facebook.py`
(the raw numbers live in [`facebook-inventory.md`](facebook-inventory.md), regenerate with
`scripts/inventory-facebook.py /mnt/photos/timelinize/ground-truth/fb --out docs/fork/facebook-inventory.md`).

**Why this could not be "derived once" before:** the export has no schema, and the *shape* of a record does not
tell you its *meaning*. Every post is `{title, timestamp, data[], attachments[]}`; what the post *is* ("wrote on
X's profile", "is feeling motivated", "added a life event", "shared a reel" with an empty URL) lives only in the
localised English prose of `title`. Messages are the same: a call, a group rename and a birthday wish are all
`{sender_name, timestamp_ms, content}`. So the catalogue needs two layers: the mechanical fingerprint (shape × title
pattern, which the script produces and which will surface anything new in a future export) and this hand-written
mapping from pattern to representation. Everything below is one row per pattern, with counts from *this* export.

Legend: ✔ represented as intended · ⚠ partially (details in the row) · ✘ not imported · — deliberately ignored.
`case` = id in `testdata/meta/messages.json` / `posts.json` (blank = no fixture yet; add one when working on the row).

## 1. Posts — `your_facebook_activity/posts/your_posts__check_ins__photos_and_videos_*.json` (390)

| # | Pattern (`title` × attachments × `data`) | n | ex. idx | Representation today | Status | case |
|---|---|---|---|---|---|---|
| P1 | `updated his status.` with `post` text | 48 | 85 | `social` item, text, Title metadata | ✔ | `fb-post-status-text` |
| P2 | `updated his status.` with no data at all | 9 | 86 | pruned (nothing to import) | ✔ | |
| P3 | `shared a link.` — `external_context.url`, with / without text | 54 / 22 | 0 / 36 | `social` + `bookmark` attachment keyed by URL | ✔ | `fb-post-shared-link-with-text`, `-no-text` |
| P4 | shared link with **empty `url`** (`shared a reel.` 9, `shared a post/photo` some, bare `ME` 3) | 31 | 258, 244 | text kept when `post` exists; otherwise pruned — the export carries nothing else | ⚠ nothing to recover; title could be kept as a note | `fb-post-shared-reel` |
| P5 | `shared a link to the group: G.` | 24 | 12 | bookmark, **group name lost** (title only) | ⚠ | |
| P6 | `shared a link / reel to X's timeline.` | 12 + 1 | 13, 346 | bookmark, **recipient lost** (only `wrote on` is parsed) | ⚠ | |
| P7 | `shared a post / photo / Page / album / episode …` (facebook permalink) | 12 / 2 / 1 / 1 / 1 | 17, 285, 52, 198, 227 | bookmark, Kind from URL, `metadata_only` (never fetched) | ✔ | |
| P8 | `added a new photo.` — single media, with text / no text | 10 / 3 | 76, 128 | `social` root + media attachment (file read directly) | ✔ | `fb-post-photo` |
| P9 | `added N new photos.` (2–23 media, ± text) | 15 | 143, 154 | root + N attachments | ✔ | `fb-post-multi-photo-no-text` |
| P10 | `added a new video.` / `N new videos` | 34 / 2 | 71, 140 | same | ✔ | `fb-post-video` |
| P11 | `added 12 photos and a video.` | 1 | 115 | same (13 attachments) | ✔ | `fb-post-many-photos-and-video` |
| P12 | media + `place` | 9 | 116, 87 | media + `attachment` edge to a place entity (coords on address attribute) | ✔ | `fb-post-video-with-place` |
| P13 | `was travelling to X.` — place only | 2 | 241 | `social` text + place entity | ✔ | `fb-post-place-only` |
| P14 | `is feeling X.` (± place, ± tags) | 6 | 196, 263, 302 | text kept; **feeling lost** (title only); place ×2 in export → two identical place attachments | ⚠ | |
| P15 | `was playing G with A and B at P.` (2 places, tags) | 1 | 300 | text + place; **activity and people lost** | ⚠ | |
| P16 | `added a life event from DATE: T.` (± description, photos, place, end_date) | 7 | 117, 118, 185 | `social` at `backdated_timestamp`, text = title + description, photos attached, place entity, Start/End date | ✔ | `fb-post-life-event*` |
| P17 | `shared an event.` / `was attending E at P.` | 2 / 1 | 88, 237 | `event` item attached, Timestamp/Timespan = event start/end | ✔ | `fb-post-event-*` |
| P18 | `wrote on X's profile.` (text only) | 33 | 344 | `social` you own + `sent` edge to X | ✔ | `fb-post-wrote-on-timeline` |
| P19 | `added a new photo to X's timeline.` | 2 | 347 | media imported, **recipient lost** | ⚠ | |
| P20 | `Shared from Instagram` (media ± place ± tags) | 16 | 166, 156 | media (+ place); cross-post origin only in Title | ✔ | `fb-post-shared-from-instagram` |
| P21 | `added an item to the featured collection: C.` (media, no text, no data) | 11 | 333 | media attached; **collection name lost** | ⚠ | `fb-post-featured-item` |
| P22 | `posted something via <app>.` (nothing else) | 16 | 91 | pruned | ✔ | `fb-post-empty` |
| P23 | `recommends X.` (page recommendation; 1 with text) | 3 | 216, 228 | 2 pruned (title only), 1 keeps text; **recommended page lost** | ⚠ | |
| P24 | `text` attachment (Friendversary "celebrating 3 years of friendship" + video) | 1 | 247 | video attached; text attachment kept only if ≠ description | ⚠ | |
| P25 | `external_context` with `name,source,url` (old layout) | 1 | 52 | bookmark with Title | ✔ | |
| P26 | `tags[]` — people tagged in the post | 13 posts | 137, 300 | **not imported** | ✘ | |
| P27 | `data[].update_timestamp` (post was edited) + `edits_you_made_to_posts.json` (157 edit texts) | 176 | 0 | ignored | — | |

## 2. Post media, albums — `posts/media/**` (794 files), `posts/album/*.json` (14), `your_uncategorized_photos.json` (364), `your_videos.json` (244)

| # | Case | n | Representation today | Status | case |
|---|---|---|---|---|---|
| M1 | photo referenced by a post **and** an album (most album photos) | 196 | one `media` item (retrieval key fuses post attachment, album entry and the media walk); `in_collection` → `collection` named after the album | ✔ | `fb-album-photo` |
| M2 | album-only photos (cover photos, profile pictures, app albums) | 19 + 3 | media + collection | ✔ | |
| M3 | album `cover_photo` | 14 albums | not marked | ✘ (minor) | |
| M4 | album `description` (1 album), photo `description` | | Description metadata | ✔ | |
| M5 | video albums (`media_variants`, `dubbing_info` keys; 44 files also in `your_videos`) | 2 albums | media + collection; extra keys ignored | ✔ | |
| M6 | app-generated albums ("Which meme are you??", "Twibbon Photos"…) | 6 | imported as collections | ✔ (junk but faithful) | |
| M7 | uncategorized photos (manifest gives `creation_timestamp`, `upload_ip`) | 359 | `media`, dated from manifest, Upload IP | ✔ | `fb-uncategorized-photo` |
| M8 | videos only in `your_videos.json` (title, description, `upload_timestamp`) | 183 | `media`, Title/Description | ✔ | |
| M9 | Messenger stickers stored under `posts/media/stickers_used/` (referenced by 484 messages, 11 files by nothing) | 11 | media walk imports them; message sticker fuses via retrieval key | ✔ | |
| M10 | **file placed by import date** (`data/2026/08/…`) when the media walk stores the bytes before the manifest supplies the date | many | cosmetic: `data_file` path ≠ item date | ⚠ | |
| M11 | `media_used_for_memories.json` (19), `shared_memories.json` (2: Friendversary videos with title/description) | 21 | not read | — | |

## 3. Messenger — `your_facebook_activity/messages/` (1,123 threads, 774,818 messages)

| # | Case | n | ex. (thread @ ts) | Representation today | Status | case |
|---|---|---|---|---|---|---|
| T1 | folders `inbox`, `archived_threads`, `filtered_threads`, `message_requests`, `e2ee_cutover` | 884 / 15 / 87 / 24 / 113 threads | | all walked | ✔ | `fb-msg-*-thread` |
| T2 | media folders `messages/photos/` (49), `messages/stickers_used/` (1,123) | | files read through the message `uri`s | ✔ | |
| T3 | group threads (>2 participants) | 263 | one `collection` item per thread (Kind: thread, `sent` edges to every participant once); every message `in_collection` of it, no per-message `sent` fan-out; `/conversations?thread=<id>` | ✔ | `fb-msg-group-basic`, `-group-left` |
| T4 | plain text | 661k | | `message` text | ✔ | `fb-msg-text-with-reaction` |
| T5 | text that is **only a URL** | 16,142 | hassanfans @ 1632003784052 | text kept **and** a `bookmark` attached (same code path as a share; not fetched) | ✔ | `fb-msg-bare-url` |
| T6 | text containing a URL | 7,248 | | text kept | ✔ | |
| T7 | `reactions[]` | 62k | | `reacted(emoji)` edge from the actor | ✔ | `fb-msg-text-with-reaction` |
| T8 | pseudo-message `Reacted X to your message` (note trailing space) | 28 | aleksandrinageorgieva @ 1659267725994 | dropped; the `reacted` edge on the target carries the pseudo-message time as `Start` | ✔ | `fb-msg-pseudo-reaction` |
| T9 | `photos[]` (1 or many per message) | 44,407 | | first photo = the message, rest attached; file read from the archive | ✔ | `fb-msg-photo`, `-photo-e2ee` |
| T10 | `photos[]` with a **bare filename** `uri` (2017 e2ee_cutover threads) | 73 | ivelinastoanova @ 1491393859348 | no item; `Missing attachments: n` on the message | ✔ | `fb-msg-attachment-missing` |
| T11 | `videos[]` | 1,689 | | attachment | ✔ | `fb-msg-video` |
| T12 | `videos[]` / `gifs[]` / `sticker` with an **https fbcdn URL** instead of a file | 18 / 2 / 2 | marinaaneva @ 1544731658122 | `bookmark` with `Kind: media`, `Status: expired` | ✔ | `fb-msg-attachment-expired-url` |
| T13 | `audio_files[]` (voice notes, .aac / audio in .mp4) | 982 | | attachment typed `audio/*` via ffprobe | ✔ | `fb-msg-audio` |
| T14 | `gifs[]` | 1,323 | | attachment | ✔ | `fb-msg-gif` |
| T15 | `sticker` (`ai_stickers` key ignored) | 11,134 | | attachment | ✔ | `fb-msg-sticker` |
| T16 | `files[]` — docx 264, pdf 116, jpg 86, doc, xlsx, pptx, rar, code… | 617 | aneliachekakchieva @ 1581942443759 | `document` attachment items (a file-only message *is* the document) | ✔ | `fb-msg-file`, `-file-docx` |
| T17 | `share.link` (± `share_text`): external 3,063 · YouTube 877 · 9gag 616 · FB video 557 · FB photo 458 · raw fbcdn 298 · FB group post 273 · IG reel 249 · FB reel 204 · FB event 173 · profile/page 86 · gif 49 · IG post 24 | 7,111 | | `bookmark` keyed by canonical URL, Kind, Status (FB pages/groups/events `metadata_only`, IG/YouTube fetchable) | ✔ | `fb-msg-share-*` |
| T18 | `share.share_text` only, no link | 37 | | ignored (share `isEmpty`) | — | |
| T19 | placeholder `X sent an attachment.` | 3,782 | borovec2021 @ 1637954078397 | dropped | ✔ | `fb-msg-share-*` |
| T20 | `call_duration` + text (`The video call ended.` …) | 1,733 | borovec2021 @ 1638201291531 | message with `Kind: call`, `Direction` (outgoing/incoming/group), `Duration`, `Missed`, `Video`, `Timespan` = start + duration | ✔ | `fb-msg-call*` |
| T21 | `missed: true` (`X missed your call.`) | 284 (855 texts) | aneliachekakchieva @ 1553270012624 | same, `Missed: true` (flag **or** duration 0 with a "missed" sentence) | ✔ | `fb-msg-missed-call`, `-call-missed-*` |
| T22 | `X sent a location.` (+ `share.link` to bing.com/maps) | 45 | alekskrsteva @ 1674988827098 | `Kind: location`; coordinates on the item when the Bing link has `where1=lat,lon`, else `Address` metadata; no bookmark | ✔ | `fb-msg-location-*` |
| T23 | `is_unsent` without content | 1,341 | | skipped | ✔ | `fb-msg-unsent` |
| T24 | `is_unsent` **with** content | 52 | bachataloverssofiachat @ 1714857435140 | dropped (`is_unsent` wins over content) | ✔ | `fb-msg-unsent-with-content` |
| T25 | `is_taken_down` | 8 | | skipped | ✔ | `fb-msg-taken-down` |
| T26 | `ip` on old messages | 13,057 | `IP` metadata on messages that have content | ✔ | `fb-msg-ip-metadata` |
| T27 | message with **no content at all** (sender + timestamp + flags) | 10,501 | hssain… @ 1718108732832 | skipped | ✔ | |
| T28 | group system messages: member added 4,204 · left 954 · named 220 · nickname 123 · removed 60 · photo 53 · pinned 9 | 5,623 | basketballcuppers @ 1623278176675 | message with `Kind: system`, `Event` (member_added/left/removed, renamed, created, photo_changed, admin), `Subject`; muted line in the chat view | ✔ | `fb-msg-group-event-*` |
| T29 | system: `You are now connected on Messenger` (11), waves (4), polls (1) | 16 | dropped, with nickname/theme/pin/emoji/moderation notices | ✔ | `fb-msg-thread-event-*-dropped` |
| T30 | `is_geoblocked_for_viewer`, `is_unsent_image_by_messenger_kid_parent` (always false), `is_still_participant`, `magic_words` | all | | ignored | — | |
| T31 | `groups/your_group_messages/<id>.json` (1 thread, 6 msgs; same schema; thread also in inbox) | 1 | | not walked | — (duplicate) | |
| T32 | **`data_messenger_e2e/`** — separate E2EE export: 97 threads (49 empty), 9,165 messages, camelCase (`senderName, timestamp, text, type ∈ text/media/link/placeholder, media[].uri, reactions[], isUnsent`), 1,221 media (jpeg 714, mp4 388, ogg 105, gif 20, webp 7; 43 without extension missing) | 9,165 | Anelia Chekakchieva_40.json | recognized as a `facebook` import root; converted to the main shape (media by extension, placeholder → unsent) and fed through the same walker; owner = the participant present in every thread | ✔ | `fb-e2ee-*` |

## 4. Other activity families (own content that is not a post or a message)

| # | File | n | What it is | Status | Worth it? |
|---|---|---|---|---|---|
| O1 | `comments_and_reactions/comments.json` | 1,595 | comments you wrote: `commented on X's post/photo/link` (1,073), `replied to X` (225), own post (66)… comment text + timestamp, sometimes `attachments` | ✘ | **yes** — own words; `social`/`message` item with Target metadata |
| O2 | `groups/your_comments_in_groups.json` | 3,094 | same, in groups (group name in `data[].comment.group`) | ✘ | **yes** |
| O3 | `groups/group_posts_and_comments.json` | 486 | posts you made in groups (`posted in G`; `data[].post`, `attachments`) — same shape as timeline posts | ✘ | **yes** — reuse the post parser + group name |
| O4 | `posts/posts_on_other_pages_and_profiles.json` | 51 | newer-layout "wrote on X's profile" (label_values: Message, Target, Media) | ✘ | yes — same as P18 |
| O5 | `comments_and_reactions/likes_and_reactions{,_1,_2}.json` | 3,231 + 4,464 | reactions you gave (LIKE/LOVE/HAHA… + target title; new layout has the URL) | ✘ | low (no target item) |
| O6 | `events/your_event_responses.json` (joined/interested), `event_invitations.json`, `your_events.json`, `events_you_hosted.json` | 460 / 715 / 1 / 21 | events with name + start/end | ✘ | **joined** → `event` items (460) |
| O7 | `saved_items_and_collections/your_saved_items.json`, `collections.json` | 84 / 1 | saved links/videos/reels (`external_context{name,source,url}`) | ✘ | yes — bookmarks, same code path as shares |
| O8 | `stories/story_reactions.json` | 78 | "loved X's story" | ✘ | low |
| O9 | `stories/archived_stories.json` | 0 | 15-byte file containing `[object Object]` — Facebook bug | — | |
| O10 | `activity_you're_tagged_in/photos_and_videos_you're_tagged_in.json` | 74 | URL + tagger name, no media | ✘ | low (bookmark) |
| O11 | `posts/places_you_have_been_tagged_in.json` | 48 | Visit time + Place name | ✔ | `fb-tagged-place` |
| O12 | `your_places/places_you've_created.json` | 1 | | ✘ | low |
| O13 | `other_activity/pokes.json` | 69 | poker/pokee/timestamp | ✘ | low, easy |
| O14 | `polls/your_poll_votes.json` | 385 | option, poll text, creator | ✘ | low |
| O15 | `facebook_marketplace/items_sold.json` | 3 | listings with price, description, coordinates | ✘ | low |
| O16 | `pages/pages_you've_liked.json`, `pages_and_profiles_you_follow.json` | 508 / 502 | | ✘ | low |
| O17 | `connections/friends/*.json` | 649 friends + removed 41 + requests 94 | name + timestamp | ✘ | maybe: "friend since" on entities |
| O18 | `personal_information/profile_information/profile_information.json` | 1 | name, birthday, emails, phones, education, places lived | ✔ owner entity attributes | |
| O19 | `profile_update_history.json` | 485 | "updated his profile picture", "added a life event"… | ✘ | low |
| O20 | `logged_information/search/your_search_history.json` | 40 | | ✘ | low |
| O21 | `security_and_login_information/account_activity.json` | 996 | logins with IP, city, user agent | ✘ | low (could be `location`) |
| O22 | `logged_information/notifications/notifications.json` | 99 | | — | |
| O23 | everything in `ads_information`, `apps_and_websites_off_of_facebook`, `preferences`, `facebook_gaming`, `facebook_payments`, `facebook_support`, `fundraisers`, `navigation_bar`, `voting`, `logged_information/*` (generic `label_values` records) | ~120 files | settings and logs | — | |

## 5. Suggested order of attack

1. **T32 E2EE export** — 9,165 messages and 1,221 media files that never enter the timeline; small, well-defined schema
   (msgvault's `rawE2EEExport` is a ready reference).
2. **T16 `files[]`** — 617 documents; add the field, reuse the attachment path.
3. **T24 / T28 / T29 / T20 / T21** — stop importing unsent-with-content; turn group/system/call messages into typed
   events (or drop them) instead of fake text messages from "A contact".
4. **P5 / P6 / P19 / P21 / P23 / P14** — parse the remaining title patterns (group target, timeline recipient,
   featured-collection name, recommendation, feeling) the way `wrote on` is parsed.
5. **P26 tags** → edges to person entities.
6. **O1 / O2 / O3 / O4** — own comments and group posts (5,226 items of your own words).
7. **O6 joined events, O7 saved items** — `event` items and bookmarks.
8. **T10 / T12** — mark attachments whose file is not in the archive as unavailable instead of leaving `data_file` NULL.
9. **M10** cosmetic file placement; **M3** album covers.

Each row that gets worked on gets a fixture case in `testdata/meta/messages.json` / `posts.json` (selectors are the `ex.` column) so the
harness pins the representation; the inventory script is rerun on every new export to catch shapes not in this list.
