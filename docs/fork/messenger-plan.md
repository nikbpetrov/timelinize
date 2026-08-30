# Messenger import — plan

Scope: Facebook Messenger threads only (`your_facebook_activity/messages/**` and the separate `data_messenger_e2e/`).
Posts are parked; Instagram DMs come next and reuse the same rules (the message schema is shared, the importer
already is — `datasources/facebook/messages.go` serves both).

Numbers are from the full export (`docs/fork/facebook-cases.md` §3, `facebook-inventory.md` §E/§F, plus the pass
that produced the deleted-user and call figures below). Example selectors are `thread @ timestamp_ms`, usable
directly in `testdata/meta/*.json`.

## 0. What a message becomes (the model)

Unchanged skeleton — one `Graph` per message:

```
message item  (Classification message, Owner = sender, Timestamp = timestamp_ms, text = what the sender typed)
  ├─ attachment ──▶ media items (photos/videos/audio/gifs/sticker; first one *is* the message when there is no text)
  ├─ attachment ──▶ bookmark item (share.link / bare URL)  ──▶ attachment ──▶ fetched media
  ├─ quotes ──────▶ own story item (Instagram only)
  ├─ sent ────────▶ every other participant (entity)
  └─ reacted(emoji) ◀── actor (entity)
```

What changes: (1) **who** the sender/recipients are when Facebook has anonymised them, (2) a **classifier** that runs on
every message *before* the graph is built and routes it to one of a small, hard-coded set of kinds, (3) the missing
attachment types and the second export.

## 1. Identity — who sent it, who received it

| Case | n | Example | Rule |
|---|---|---|---|
| I1 named participant | most | | entity `Name`, identity attribute `facebook_name` (as now) |
| I2 **deleted profile**: sender / participant `"Facebook user"` (lower-case u), thread dir `facebookuser_<id>` | 17,982 msgs · 579 participant slots · 80 dirs · 332 reaction actors | `filtered_threads/facebookuser_3718308748248174`; `archived_threads/hssain2beds2bathsroomonly_26569962889261237` | today every deleted person collapses into **one** entity "Facebook user" (identity = name). New rule: identity attribute `facebook_thread_user` = `<thread id>` (the numeric suffix of the thread dir, or the dir itself), display name `Facebook user` — one entity per thread, never merged across threads. In a group, all deleted senders of that thread share one entity (the export gives nothing to tell them apart) |
| I3 **unnamed participant**: `participants[].name == ""`, thread dir is only digits, `title == ""` | 26 threads | `inbox/1085463558199386` (312 msgs) | same as I2 (entity per thread id); today this creates an entity with an empty name |
| I4 `"A contact"` inside system text ("A contact added participants.", "You called a contact.") | 2,718 mentions, never a sender | `message_requests/philipfazloodienfacebookuserand23others_24017573717861563 @ 1753161197891` | no entity; the text stays as-is on the system/call item |
| I5 **self thread** (only participant is you) | 1 thread, 297 msgs | `e2ee_cutover/nikolaypetrov_904946866251057` | message owned by you, no `sent` edges (today: none either — fine, add a case so it stays that way) |
| I6 sender **not in `participants`** (you left the group; list truncated to you) | `inbox/bachataloverssofiachat_6338047269546821`: 2,533 msgs, participants = [you] | | sender entity from `sender_name` as now; `sent` edges only to listed participants (= you). Record `Thread participants: partial` in metadata |
| I7 **group thread** (>2 participants; up to 249) | 263 threads | `archived_threads/basketballcuppers_4116085855122962` | today: a `sent` edge to *every* participant on *every* message (249 × msgs rows). Proposal: keep `sent` edges only in 1:1 threads; for groups attach every message to one **conversation item** per thread (`Classification collection`, text = `title`, `ID = thread dir`) via `in_collection`, and give the collection the `sent` edges to all participants once. This is also what makes group conversations renderable (backlog #24) |
| I8 marketplace threads: 1:1 but `title` = "Arjay · 2 beds 2 baths Room only" | 34 threads with title ≠ other participant | `archived_threads/arjay2beds2bathsroomonly_7650016228410023` | title → `Thread title` metadata on the collection/messages; entity name still from `participants` |
| I9 thread flags `is_still_participant`, `is_pending` (message requests), `joinable_mode`, `image` (group photo), `magic_words` | all | | `is_pending` → metadata `Message request: true`; `image` → collection picture; rest ignored |

## 2. Classifier — hard-coded kinds, decided from `content` + fields before anything else

Order matters; first match wins. Everything not matched is a normal message.

| Kind | Trigger | n | Example | Representation |
|---|---|---|---|---|
| K1 `placeholder` | `content` = "X sent an attachment." | 3,782 | `archived_threads/borovec2021_4186676704788330 @ 1637954078397` | text dropped (as now) |
| K2 `pseudo_reaction` | `content` = "Reacted 😂 to your message" / "Liked a message" (trailing space variant) | 28 (+1,031 on IG) | `inbox/aleksandrinageorgieva_5349098711822339 @ 1659267725994` | **drop** the message; the real reaction already exists in `reactions[]` of the target. Set `Start` on the `reacted` edge from this message's timestamp when a target with matching actor exists (closes #25) |
| K3 `call` | `call_duration` present (1,733) — texts: "The video call ended." (group), "You called X.", "X called you.", "You missed a call from X.", "X missed your call.", "You missed a video call with X.", "You called a contact." | 1,733 | `e2ee_cutover/marinaaneva_887455161333561 @ 1702815411658`, `… @ 1699953759318` (missed) | `message` item, text = original sentence, metadata `Kind: call`, `Direction: outgoing/incoming/group` (from the sentence), `Duration: <s>`, `Missed: true` when `missed` **or** duration 0 with a "missed" sentence, `Video: true` when the sentence says video; `Timespan = timestamp + duration`. Owner = sender as exported |
| K4 `call_event` | "X joined the video call.", "X started sharing video" (no `call_duration`) | 40 | `archived_threads/borovec2021_4186676704788330 @ 1638201283096` | `Kind: system`, `Event: call_joined` |
| K5 `group_event` | "X added Y to the group." 4,204 · "X left the group." 954 · "X named the group N." 220 · "X created the group." 103 · "X removed Y from the group." 60 · "X changed the group photo." 53 · "X is now an admin." 3 | 5,597 | `archived_threads/basketballcuppers_4116085855122962 @ 1623278176675` | `message` item with `Kind: system`, `Event: member_added / member_left / renamed / created / member_removed / photo_changed / admin`, `Subject: Y` where parsed, attached to the thread collection. Kept because "you joined/left this group on <date>" is timeline-worthy; the UI filters `Kind: system` out of the chat bubbles |
| K6 `thread_event` | nickname set/cleared 150 · theme 43 · pin/unpin 14 · emoji · "You are now connected on Messenger" 11 · waves 4 | 222 | `archived_threads/weekendfootball_152529788435989 @ 1517690034336` | **drop** (cosmetic chat state) — or `Kind: system` with `Event: …` if you prefer symmetry; recommendation: drop |
| K7 `location` | "X sent a location." + `share.link` to `bing.com/maps…where1=` | 45 | `e2ee_cutover/alekskrsteva_4677096612353993 @ 1674988827098` | `message` with `Item.Location` when `where1` is `lat, lon`; when it is an address, metadata `Address` (from `share_text`, decoded); no bookmark |
| K8 `unsent` | `is_unsent` (1,341 without content; 52 with content "This poll is no longer available.") | 1,393 | `inbox/bachataloverssofiachat_6338047269546821 @ 1714857435140` | drop (today the 52 with content are imported as text) |
| K9 `taken_down` | `is_taken_down` | 8 | | drop (as now) |
| K10 `empty` | nothing but sender + timestamp (+ flags) | 10,501 | `archived_threads/hssain2beds2bathsroomonly_26569962889261237 @ 1718108732832` | drop (as now) |
| K11 `share` | `share.link` (± `share_text`) | 7,111 | existing `fb-msg-share-*` cases | bookmark keyed by canonical URL (as now); `share_text`-only (37) → metadata `Share text` on the message, no bookmark |
| K12 `link` | `content` is **exactly one URL** | 16,142 | `archived_threads/hassanfans_3183226831782158 @ 1632003784052` | text kept **and** a bookmark attached, same code path as K11 (Kind/Status from the URL; fetching only for kinds the resolver handles, as today). Text containing a URL among words (7,248) stays plain text |
| K13 `message` | everything else | 661k | | as now |

Attachments are processed for every kind except K2/K8/K9/K10 (K1 keeps its attachments — that is the point).

## 3. Attachments

| Case | n | Example | Rule |
|---|---|---|---|
| A1 `photos[]`, `videos[]`, `audio_files[]`, `gifs[]`, `sticker` with an archive path | 59k | existing cases | as now (first attachment becomes the message when there is no text) |
| A2 **`files[]`** (docx 264, pdf 116, jpg 86, doc, xlsx, pptx, rar, code, epub…) | 617 | `e2ee_cutover/aneliachekakchieva_890711401007937 @ 1581942443759` | attachment item, `Classification document` (images among them stay `media` by sniffed type), filename in metadata |
| A3 `uri` is a **bare filename** (2017 threads) | 73 | `e2ee_cutover/ivelinastoanova_1096863243674856 @ 1491393859348` | not in the archive: no item; message gets metadata `Missing attachments: n` (today: an item with `data_file NULL`) |
| A4 `uri` is an **https fbcdn/fbsbx URL** (expired CDN) | 22 | `e2ee_cutover/marinaaneva_887455161333561 @ 1544731658122` | bookmark with `Status: expired`, Kind `media` — same as an unavailable share |
| A5 `ip` on the message | 13,057 | `fb-msg-ip-key` | metadata `IP` (cheap, occasionally useful for where-was-I) |
| A6 `is_geoblocked_for_viewer`, `is_unsent_image_by_messenger_kid_parent`, `sticker.ai_stickers` | all | | ignored |

## 4. The E2EE export — `data_messenger_e2e/`

97 thread files (49 empty), 9,165 messages, 1,221 media files. It is the **continuation** of the `e2ee_cutover`
threads: for all 31 people present in both, every E2EE message is later than the last cutover message (no overlap);
17 threads exist only here. Different schema, so a second walker feeding the same per-message code:

| Field | Meaning | Rule |
|---|---|---|
| file `<Full Name>_<n>.json`, `threadName`, `participants: [names]` | 1:1 threads only in this export | other participant from `participants`; same entity as the cutover thread (same `facebook_name`) |
| `senderName`, `timestamp` (ms), `text` | | as main export |
| `type: text` (7,392) | | K12/K13 |
| `type: link` (460) — `text` is the URL | `Anelia Chekakchieva_40.json @ 1745939437363` | K12 |
| `type: media` (1,277) — `media[].uri = ./media/<uuid>.<jpeg\|mp4\|ogg\|gif\|webp>` (43 are the literal string `Failed to download media`) | `… @ 1763050838383` | A1; `Failed to download media` → A3 |
| `type: placeholder` (36) — `isUnsent: true`, text "User unsent a message" | `Blagovesta Dlagatseva_27.json @ 1729801020288` | K8 |
| `reactions[] {actor, reaction}` (1,123) | | as main export |
| `.ogg` voice notes | 105 | audio (ffprobe typing already handles) |

No calls, no system events, no shares in this format (they arrive as `text`/`link`).

## 5. Things deliberately left alone

`groups/your_group_messages/<id>.json` (1 thread, duplicate of an inbox thread) · `messages/*.json` settings files ·
`magic_words`, `joinable_mode` · the `photos/` and `stickers_used/` folders are only reached through message `uri`s
(as now).

## 6. Test/dev restructuring (messages only)

- `testdata/meta/cases.json` → split into **`testdata/meta/messages.json`** (active) and `testdata/meta/posts.json`
  (parked, unchanged content). The builder, the Go harness, `dev-reset.sh` and the Playwright specs take
  `TLZ_CASES` (default `messages`; `all` runs everything). Instagram DM cases move to `messages.json` too but stay
  as they are until the IG pass.
- Selector additions in the manifest: `threads: [{thread, all: true}]` copies a **whole** thread (needed for
  thread-level cases: deleted user, self thread, marketplace title, group-with-left, message requests) and
  `e2ee: [{file, ts: [...]}]` for the E2EE export. `where` gains `metadata` matching and `kind`.
- Fixture: same fixture directory; posts/albums/media/tagged-places no longer copied when `TLZ_CASES=messages`.
- Harness checks per case as now; new global checks: no entity named `Facebook user` shared by two threads, no
  entity with an empty name, no message whose text matches a system pattern without `Kind: system`.
- Playwright: conversation spec extended to group threads once I7 lands (the skip goes away).

### Representative cases to add (all from this export)

| id | selector | asserts |
|---|---|---|
| `fb-msg-deleted-user-1to1` | thread `filtered_threads/facebookuser_3718308748248174` (whole) | sender entity has `facebook_thread_user` identity, name `Facebook user`; a second deleted-user thread yields a different entity |
| `fb-msg-deleted-user-in-group` | `archived_threads/hssain2beds2bathsroomonly_26569962889261237` (whole) | messages from "Facebook user" owned by the per-thread entity; thread title kept |
| `fb-msg-unnamed-participant` | `inbox/1157564424322632` (whole, 1 msg) | no empty-name entity |
| `fb-msg-self-thread` | `e2ee_cutover/nikolaypetrov_904946866251057 @ <3 ts>` | owner = you, no `sent` edges |
| `fb-msg-marketplace-title` | `archived_threads/arjay2beds2bathsroomonly_7650016228410023` (whole) | `Thread title` metadata, entity = Arjay Dulay |
| `fb-msg-group-basic` | `archived_threads/basketballcuppers_4116085855122962 @ <5 ts>` | collection item = thread, `in_collection` edges, `sent` edges on the collection only |
| `fb-msg-group-left` | `inbox/bachataloverssofiachat_6338047269546821 @ <3 ts>` | senders not in participants still get entities; `Thread participants: partial` |
| `fb-msg-call-outgoing` / `-incoming` / `-missed` / `-group-ended` | `e2ee_cutover/marinaaneva_887455161333561 @ 1702815411658 / 1664703680709 / 1699953759318`, `archived_threads/borovec2021_4186676704788330 @ 1638201291531` | `Kind: call`, Direction, Duration, Missed, Timespan |
| `fb-msg-group-event-added` / `-left` / `-named` / `-created` | `archived_threads/basketballcuppers_4116085855122962 @ 1623278176675 / 1635357734076`, `archived_threads/arjay… @ 1718031325282`, `basketballcuppers @ 1622042596541` | `Kind: system`, `Event`, `Subject` |
| `fb-msg-thread-event-dropped` | `archived_threads/weekendfootball_152529788435989 @ 1517690034336` (nickname), `mentalistite… @ 1689138890562` (theme) | count 0 |
| `fb-msg-location-coords` / `-address` | `e2ee_cutover/alekskrsteva_4677096612353993 @ 1674988827098`, `<Dimitar thread> @ <ts>` | `Item.Location` set / `Address` metadata |
| `fb-msg-unsent-with-content` | `inbox/bachataloverssofiachat_6338047269546821 @ 1714857435140` | count 0 |
| `fb-msg-bare-url` | `archived_threads/hassanfans_3183226831782158 @ 1632003784052` | text kept + bookmark attachment |
| `fb-msg-file-docx` / `-pdf` | `e2ee_cutover/aneliachekakchieva_890711401007937 @ 1581942443759`, `<pdf ts>` | `document` attachment with data |
| `fb-msg-attachment-missing` | `e2ee_cutover/ivelinastoanova_1096863243674856 @ 1491393859348` | no data-less item; `Missing attachments` metadata |
| `fb-msg-attachment-expired-url` | `e2ee_cutover/marinaaneva_887455161333561 @ 1544731658122` | bookmark `Status: expired` |
| `fb-msg-share-text-only` | one of the 37 | `Share text` metadata, no bookmark |
| `fb-msg-pseudo-reaction` (flip) | existing | count 0; `reacted` edge on the target has `Start` |
| `fb-e2ee-text` / `-link` / `-media` / `-voice` / `-placeholder` / `-failed-media` / `-reaction` | `Anelia Chekakchieva_40.json @ …`, `Blagovesta Dlagatseva_27.json @ 1729801020288`, … | E2EE walker output matches the main-export representation |
| `fb-e2ee-continues-cutover` | Anelia: last cutover msg + first E2EE msg | both owned by the same entity |

Existing message cases (`fb-msg-text-with-reaction`, `-photo`, `-video`, `-gif`, `-audio`, `-sticker`, `-share-*`,
`-unsent`, `-taken-down`, `-ip-key`, `-e2ee-thread`, `-archived-thread`, `-filtered-thread`, `-message-requests`) stay.

## 7. Execution order (each step = code + cases + green harness + dev-reset)

1. **Restructure tests** (§6): manifest split, `TLZ_CASES`, whole-thread and E2EE selectors, rebuild fixture.
   Nothing changes in the importer yet; all existing cases stay green.
2. **Identity** (I2, I3, I5, I6, I8, I9): per-thread deleted/unnamed entities, thread title metadata. Small, high value
   (18k messages currently attributed to one fake person).
3. **Classifier** (K1–K13) as one function `classifyMessage(msg) (kind, fields)` with a table-driven test in
   `datasources/facebook` on the sentences above — the hard-coded rules live in one place, and Instagram's
   sentences are added to the same table later.
4. **Calls, system events, drops** (K3–K6, K8) wired through the classifier.
5. **Attachments** (A2–A5): `files[]`, missing/expired handling, IP metadata.
6. **Bare URLs and share-text-only** (K12, K11 tail).
7. **E2EE walker** (§4).
8. **Group threads as collections** (I7) + frontend conversation view for groups (#24) + hiding `Kind: system` bubbles.
9. Re-import the full export on the main server and diff counts against the inventory (`verify-import.py`).

## 8. Decisions I need from you

1. K5/K6: keep group membership events as `Kind: system` messages (recommended) or drop everything that isn't a
   person talking?
2. I7: group thread = a `collection` item that carries the participants (recommended, and it fixes the UI), or keep
   the per-message `sent` fan-out?
3. K12: attach bookmarks to bare-URL messages (16k bookmarks, no fetching unless the kind is fetchable) — yes/no?
4. A5: keep the `IP` metadata — yes/no?
