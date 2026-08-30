#!/usr/bin/env bash
# Import the dev subset of both Meta exports into the dev repo (server on :12003 must be up).
# Pass extra `timelinize import` flags as arguments. LF_MAX = link fetches per import (default 5).
set -euo pipefail
DEV=/mnt/photos/timelinize/repo-dev
export TLZ_ADMIN_ADDR=127.0.0.1:12003
REPO=$(timelinize open-repositories | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["instance_id"])')
echo "dev repo $REPO"

UC="--job.processing_options.item_unique_constraints.filename true \
    --job.processing_options.item_unique_constraints.timestamp true \
    --job.processing_options.item_unique_constraints.latlon true \
    --job.processing_options.item_unique_constraints.classification_name true \
    --job.processing_options.item_unique_constraints.data true \
    --job.processing_options.link_fetch.enabled true \
    --job.processing_options.link_fetch.max_per_import ${LF_MAX:-5}"
GT=/mnt/photos/timelinize/ground-truth

timelinize import --repo "$REPO" $UC --job.plan.files[0].data_source_name instagram \
  --job.plan.files[0].filenames[0] $GT/ig \
  --job.plan.files[0].data_source_options.max_posts 3 --job.plan.files[0].data_source_options.max_stories 5 \
  --job.plan.files[0].data_source_options.conversations[0] rhys_613578166303284 \
  --job.plan.files[0].data_source_options.conversations[1] stanislavrangelov_1329792411755940 \
  --job.plan.files[0].data_source_options.max_messages_per_conversation 15 "$@" >/dev/null
timelinize import --repo "$REPO" $UC --job.plan.files[0].data_source_name facebook \
  --job.plan.files[0].filenames[0] $GT/fb/data \
  --job.plan.files[0].data_source_options.max_posts 10 \
  --job.plan.files[0].data_source_options.conversations[0] deyanakostova_1686851964727206 \
  --job.plan.files[0].data_source_options.conversations[1] loragerova_8014936955182913 \
  --job.plan.files[0].data_source_options.max_messages_per_conversation 15 "$@" >/dev/null

# wait for both jobs to finish
for i in $(seq 1 600); do
  n=$(python3 -c "import sqlite3;print(sqlite3.connect('$DEV/timeline.db').execute(\"select count(*) from jobs where type='import' and state in ('queued','started')\").fetchone()[0])" 2>/dev/null || echo 1)
  [ "$n" = 0 ] && break; sleep 1
done
python3 "$(dirname "$0")/dev-counts.py"
