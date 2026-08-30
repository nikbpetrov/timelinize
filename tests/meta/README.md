# Meta import tests

`go test ./tests/meta` imports the testing fixture (a mini Instagram + Facebook export) into a
temporary repository with the **real pipeline** and checks every case in `testdata/meta/cases.json`.
It takes ~4 s. No server, no network (link fetching and Immich are off).

```
go test ./tests/meta                                          # fresh temp repo from the fixture
go test ./tests/meta -run 'TestMeta/ig-msg-pseudo-reaction'   # one case
TLZ_TEST_REPO=/mnt/photos/timelinize/repo-dev go test ./tests/meta   # check an existing repo (e.g. after scripts/dev-reset.sh)
TLZ_TEST_KEEP=1 go test ./tests/meta                          # keep the temp repo to poke at (path is logged)
TLZ_TESTDATA=/elsewhere/testing-data                          # fixture location
```
Always build with the pinned toolchain: `PKG_CONFIG_PATH=/usr/local/lib/pkgconfig GOTOOLCHAIN=go1.25.8 CGO_ENABLED=1 go test ./tests/meta`.

## Adding a case
1. Find the record in the ground-truth export (thread + `timestamp_ms`, post index, story file name).
2. Add a case to `testdata/meta/cases.json` with `select` and `expect`.
3. `scripts/build-testing-data.py` (rebuilds `/mnt/photos/timelinize/testing-data`), then `go test ./tests/meta`.
4. If the current behaviour is wrong, write the expectation for the **desired** behaviour, fix the code, and
   keep the case; if it cannot be fixed now, describe the current behaviour and add `known_issue`.
5. `scripts/dev-reset.sh` imports the fixture into the dev server; `cd tests/ui && npx playwright test` opens every
   case in the UI.

## Case shape
```json
{ "id": "ig-msg-pseudo-reaction", "source": "instagram", "entity": "message",
  "subtype": "short label", "why": "what makes this case different", "known_issue": "optional",
  "select": { "messages": [{"thread": "inbox/x_123", "ts": [1706981744029]}], "posts": [3], "stories": ["18037091606206114.jpg"],
              "tagged_places": [0], "albums": [{"file": "0.json", "photos": [0]}], "uncategorized_photos": [0], "videos": [0] },
  "expect": { "items": [ { "where": {...}, "count": 1, ...assertions } ] } }
```
`select.messages[].thread` is relative to the export's messages folder (`inbox/…`, `e2ee_cutover/…`, `message_requests/…`).
`posts` indexes into the concatenated `posts_N.json` files of the full export.

## Expectation vocabulary
`where` picks items of the case's data source (every field set must match):
`ts` (exact `items.timestamp`, ms), `classification`, `has_text`, `has_file`, `is_root` (not the target of any
relationship), `data_text_contains`, `data_file` (suffix), `data_file_contains`, `data_type_prefix`.

Attachments carry the **media's own `creation_timestamp`** (second precision), not the message's `timestamp_ms`;
select them by `data_file`.

Assertions on each matched item: `count` (exact; `0` asserts absence; omitted = at least one), `classification`,
`data_text` (string, or `null` = must be empty), `data_text_contains`, `has_text`, `data_file` (`true`/`false` or a suffix),
`data_type_prefix`, `owner` (entity name or identity attribute value, e.g. the username), `metadata` (subset, values
compared as strings), `metadata_has` (keys), `edges_out` / `edges_in` (each must match at least one relationship:
`label`, `value`, `to_entity`/`from_entity` (name or identity value), `to_entity_contains`, `to_entity_type`,
`to_item`/`from_item` {`classification`, `data_text`, `data_text_contains`, `data_file`, `data_file_contains`}),
`edges_out_count` {label: n}.

`checks` are global SQL invariants (`select count(*) …` must equal `expect`), e.g. no mojibake entities, no message
whose text is a bare reel link without its bookmark, no media without a file, no empty unlinked items.

Failures print the actual rows and their edges, so most fixes are a matter of reading the message.
