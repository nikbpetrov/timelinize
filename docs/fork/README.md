# Fork notes — index

Working notes for the `nikbpetrov/timelinize` fork. Quick architecture reference is `../../CLAUDE.md`.

| File | What |
|---|---|
| `plan.md` | Phased plan, current decisions, what's next |
| `backlog.md` | Bugs/limitations found, with file:line and observed impact; what's fixed on the fork |
| `exports.md` | What is actually in the ground-truth exports (Instagram, Facebook), reconciliation numbers, cookie handling |
| `link-fetching.md` | Shared-link handling: URL taxonomy, yt-dlp / gallery-dl trial results, resolver design |
| `immich-media-store.md` | Immich as canonical media store: design, verified API behaviour, permissions, open items |

Dev workflow (details in `CLAUDE.md`): main server :12002 -> `/mnt/photos/timelinize/repo` (full IG import);
dev server :12003 (`XDG_CONFIG_HOME=/root/.config/timelinize-dev`) -> `repo-dev`, rebuilt in ~10 s from the
filtered imports below. Ground truth is read-only under `/mnt/photos/timelinize/ground-truth/`.

```
# dev import (both sources), run against :12003
UC="--job.processing_options.item_unique_constraints.filename true \
    --job.processing_options.item_unique_constraints.timestamp true \
    --job.processing_options.item_unique_constraints.latlon true \
    --job.processing_options.item_unique_constraints.classification_name true \
    --job.processing_options.item_unique_constraints.data true"
timelinize import --repo <id> $UC --job.plan.files[0].data_source_name instagram \
  --job.plan.files[0].filenames[0] /mnt/photos/timelinize/ground-truth/ig \
  --job.plan.files[0].data_source_options.max_posts 3 --job.plan.files[0].data_source_options.max_stories 5 \
  --job.plan.files[0].data_source_options.conversations[0] rhys_613578166303284 \
  --job.plan.files[0].data_source_options.conversations[1] stanislavrangelov_1329792411755940 \
  --job.plan.files[0].data_source_options.max_messages_per_conversation 15
timelinize import --repo <id> $UC --job.plan.files[0].data_source_name facebook \
  --job.plan.files[0].filenames[0] /mnt/photos/timelinize/ground-truth/fb/data \
  --job.plan.files[0].data_source_options.conversations[0] deyanakostova_1686851964727206 \
  --job.plan.files[0].data_source_options.conversations[1] loragerova_8014936955182913 \
  --job.plan.files[0].data_source_options.max_messages_per_conversation 15
```

Expected dev counts: instagram social 15 / media 5 / message 26; facebook message 30; 7 entities, 0 mojibake.
