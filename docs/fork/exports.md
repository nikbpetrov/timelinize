# Ground-truth exports — what's actually in them

Location: `/mnt/photos/timelinize/ground-truth/` (NFS, read-only by convention). Never modified by imports.

## Instagram (`ig/`, 2025 layout, 803 files, 605 MB)
- Recognized by `personal_information/personal_information/instagram_profile_information.json` + `your_instagram_activity/`.
- Posts: `your_instagram_activity/media/posts_1.json` — 28 posts, 60 media, Oct 2020 -> Jan 2022, all with captions.
- Stories: `.../media/stories.json` — 356, Oct 2020 -> Aug 2025 (215 MB).
- Messages: `your_instagram_activity/messages/{inbox (144 threads), message_requests}` — 7,704 inbox messages;
  186 thread files total; 124 attachment files (photos/videos/audio) under `<thread>/{photos,videos,audio}`.
- Profile username: `nik.b.petrov`; DM identities are display names only (no user ids anywhere).
- Text is double-encoded UTF-8 (mojibake in the raw JSON); `facebook.FixString` decodes it correctly.
- Full import (2026-08-29): 8,005 items = 7,561 messages + 356 media + 88 social; 153 entities; 9,395 relationships; 291 MB.

### Reconciliation (why 7,561 messages and not 7,748)
7,555 inbox roots (text or attachment) - 113 attachment-only roots + 124 attachments - 5 timestamp collisions = 7,561.
182 messages in `message_requests` were not walked (fixed on fork). The 5 collisions are ID-less messages with identical
`timestamp_ms` merged by the `timestamp+data` unique constraints.

### Share links inside DMs (full repo)
2,965 of 7,561 messages contain a share link; 1,392 are the placeholder "You sent an attachment.".
By kind: `reel` 2,307 / `p` (photo or carousel post) 357 / `stories` 302 / `tv` 1.

## Facebook (`fb/`, 5.5 GB)
- `fb/data/` (5.0 GB) is the standard 2024+ layout; import root must be `fb/data` (profile at
  `personal_information/profile_information/profile_information.json`, username `nikbpetrov`).
- **No posts and no stories**: `your_facebook_activity/posts/` contains only empty `album/` and `media/`; `stories/` is empty.
  Only messages are importable. A new export with "Posts" selected is needed for own posts.
- Messages: `your_facebook_activity/messages/{inbox, archived_threads, message_requests, filtered_threads, e2ee_cutover}`
  — same JSON schema as Instagram. `e2ee_cutover` holds threads migrated to E2EE (e.g. `loragerova_8014936955182913`,
  669 msgs, 126 attachments, 41 shares). Upstream walked only `inbox` + `archived_threads`.
- Other native data currently ignored by the importer: `events/` (`your_events.json`, `events_you_hosted.json` 21,
  `event_invitations.json` 715, `your_event_responses.json`), `groups/`, `comments_and_reactions/`.
- `fb/data_messenger_e2e/` (477 MB, 98 threads): **different schema** `{participants, threadName, messages}` + `media/` —
  the Messenger E2EE export. No importer handles it.
- FB share links in DMs (all threads): groups 262 / reel 199 / events 152 / permalink 50 / photo 6 / page posts.

## Cookies (for link fetching)
- Provided as Firefox "Cookie Quick Manager" JSON (`Host raw`, `Name raw`, `Content raw`, `Path raw`, `Expires raw`,
  `Send for raw`, `This domain only raw`). Converted by script (values never printed) to Netscape `cookies.txt`.
- Stored at `/root/.config/timelinize/cookies/{instagram,facebook}.txt` (+ original `.json`), mode 600, off the data mount.
- Instagram: 9 cookies for `.instagram.com`; Facebook: 9 for `.facebook.com`. Sessions get invalidated by Meta on
  suspicious use -> re-export when fetches start failing with auth errors.
