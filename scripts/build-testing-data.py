#!/usr/bin/env python3
"""Build the Meta testing fixture: a mini Instagram + Facebook export containing only the
records selected by testdata/meta/cases.json, copied verbatim (bytes untouched) from the
ground-truth exports into the real export layout, so the importers run unchanged.

  scripts/build-testing-data.py                # -> /mnt/photos/timelinize/testing-data
  scripts/build-testing-data.py --out DIR --ground-truth DIR

Output layout:
  <out>/instagram/            2025 Instagram layout (import root)
  <out>/facebook/data/        2024+ Facebook layout (import root)
  <out>/MANIFEST.md           what was copied, per case

Re-running wipes and rebuilds <out>/instagram and <out>/facebook (a marker file guards
against deleting a folder we did not create).
"""
import argparse, json, os, shutil, sys, glob, collections

HERE = os.path.dirname(os.path.abspath(__file__))
MANIFEST = os.path.join(HERE, '..', 'testdata', 'meta', 'cases.json')
MARKER = '.generated-by-build-testing-data'

IG_PROFILE = 'personal_information/personal_information/personal_information.json'
IG_PROFILE2 = 'personal_information/personal_information/instagram_profile_information.json'
IG_POSTS = 'your_instagram_activity/media/posts_%d.json'
IG_STORIES = 'your_instagram_activity/media/stories.json'
IG_MESSAGES = 'your_instagram_activity/messages'
FB_PROFILE = 'personal_information/profile_information/profile_information.json'
FB_POSTS = 'your_facebook_activity/posts/your_posts__check_ins__photos_and_videos_%d.json'
FB_TAGGED = 'your_facebook_activity/posts/places_you_have_been_tagged_in.json'
FB_ALBUMS = 'your_facebook_activity/posts/album'
FB_UNCAT = 'your_facebook_activity/posts/your_uncategorized_photos.json'
FB_VIDEOS = 'your_facebook_activity/posts/your_videos.json'
FB_MESSAGES = 'your_facebook_activity/messages'


class Builder:
    def __init__(self, src, dst, name):
        self.src, self.dst, self.name = src, dst, name
        self.copied = set()
        self.missing = []
        self.per_case = collections.defaultdict(list)
        self.case = None

    def path(self, rel):
        return os.path.join(self.src, rel)

    def load(self, rel):
        with open(self.path(rel), encoding='utf-8') as f:
            return json.load(f)

    def write_json(self, rel, obj):
        p = os.path.join(self.dst, rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, 'w', encoding='utf-8') as f:
            json.dump(obj, f, ensure_ascii=False, indent=2)  # ensure_ascii=False keeps Meta's mojibake bytes exactly as read
        self.per_case[self.case].append(rel)

    def copy_file(self, rel):
        if not rel or rel in self.copied:
            return
        s, d = self.path(rel), os.path.join(self.dst, rel)
        if not os.path.isfile(s):
            self.missing.append((self.case, rel))
            return
        os.makedirs(os.path.dirname(d), exist_ok=True)
        shutil.copy2(s, d)
        self.copied.add(rel)
        self.per_case[self.case].append(rel)

    def copy_media_of(self, obj):
        """Copy every file referenced by a 'uri' anywhere inside obj (posts, messages, albums...)."""
        if isinstance(obj, dict):
            for k, v in obj.items():
                if k == 'uri' and isinstance(v, str) and '/' in v:
                    self.copy_file(v)
                else:
                    self.copy_media_of(v)
        elif isinstance(obj, list):
            for v in obj:
                self.copy_media_of(v)


def load_posts(b, pattern):
    posts = []
    for i in range(1, 1000):
        rel = pattern % i
        if not os.path.exists(b.path(rel)):
            break
        posts += b.load(rel)
    return posts


def select_messages(b, msg_root, thread, timestamps):
    """Return the thread JSON with only the selected messages (order preserved), from all message_N.json parts."""
    files = sorted(glob.glob(os.path.join(b.src, msg_root, thread, 'message_*.json')))
    if not files:
        raise SystemExit(f'{b.name}: thread not found: {thread}')
    want = set(timestamps)
    out, found = None, []
    for f in files:
        with open(f, encoding='utf-8') as fh:
            data = json.load(fh)
        if out is None:
            out = {k: v for k, v in data.items() if k != 'messages'}
        found += [m for m in data['messages'] if m.get('timestamp_ms') in want]
    got = {m['timestamp_ms'] for m in found}
    for ts in want - got:
        b.missing.append((b.case, f'{thread} timestamp_ms={ts}'))
    found.sort(key=lambda m: -m['timestamp_ms'])  # newest first, as in the export
    out['messages'] = found
    return out


def build(manifest, gt, out):
    ig = Builder(os.path.join(gt, manifest['sources']['instagram']['export']), os.path.join(out, 'instagram'), 'instagram')
    fb = Builder(os.path.join(gt, manifest['sources']['facebook']['export']), os.path.join(out, 'facebook', 'data'), 'facebook')
    builders = {'instagram': ig, 'facebook': fb}

    for b in builders.values():
        if os.path.isdir(b.dst) and not os.path.exists(os.path.join(b.dst, MARKER)):
            raise SystemExit(f'refusing to wipe {b.dst}: no {MARKER} marker (not created by this script)')
        shutil.rmtree(b.dst, ignore_errors=True)
        os.makedirs(b.dst)
        open(os.path.join(b.dst, MARKER), 'w').write('generated by scripts/build-testing-data.py; safe to delete\n')

    # accumulate selections per source
    sel = {s: collections.defaultdict(list) for s in builders}
    msg_sel = {s: collections.defaultdict(set) for s in builders}   # thread -> {ts}
    case_of = {s: collections.defaultdict(list) for s in builders}  # (kind, key) -> [case ids]
    for case in manifest['cases']:
        s = case['source']
        for kind, keys in case['select'].items():
            if kind == 'messages':
                for m in keys:
                    msg_sel[s][m['thread']].update(m['ts'])
                    case_of[s][('messages', m['thread'])].append(case['id'])
            else:
                for k in keys:
                    key = json.dumps(k, sort_keys=True)
                    if key not in [json.dumps(x, sort_keys=True) for x in sel[s][kind]]:
                        sel[s][kind].append(k)
                    case_of[s][(kind, key)].append(case['id'])

    # ---- Instagram
    b = ig
    b.case = '(profile)'
    b.copy_file(IG_PROFILE)
    b.copy_file(IG_PROFILE2)
    prof = b.load(IG_PROFILE)
    b.copy_media_of(prof)
    posts = load_posts(b, IG_POSTS)
    chosen = []
    for idx in sel['instagram']['posts']:
        b.case = ','.join(case_of['instagram'][('posts', json.dumps(idx))])
        chosen.append(posts[idx]); b.copy_media_of(posts[idx])
    if chosen:
        b.case = '(posts)'
        b.write_json(IG_POSTS % 1, chosen)
    stories = b.load(IG_STORIES)['ig_stories']
    by_name = {os.path.basename(s['uri']): s for s in stories}
    chosen = []
    for name in sel['instagram']['stories']:
        b.case = ','.join(case_of['instagram'][('stories', json.dumps(name))])
        if name not in by_name:
            b.missing.append((b.case, 'story ' + name)); continue
        chosen.append(by_name[name]); b.copy_media_of(by_name[name])
    if chosen:
        b.case = '(stories)'
        b.write_json(IG_STORIES, {'ig_stories': chosen})
    for thread, tss in msg_sel['instagram'].items():
        b.case = ','.join(case_of['instagram'][('messages', thread)])
        data = select_messages(b, IG_MESSAGES, thread, tss)
        b.write_json(f'{IG_MESSAGES}/{thread}/message_1.json', data)
        b.copy_media_of(data['messages'])

    # ---- Facebook
    b = fb
    b.case = '(profile)'
    b.copy_file(FB_PROFILE)
    posts = load_posts(b, FB_POSTS)
    chosen = []
    for idx in sel['facebook']['posts']:
        b.case = ','.join(case_of['facebook'][('posts', json.dumps(idx))])
        chosen.append(posts[idx]); b.copy_media_of(posts[idx])
    if chosen:
        b.case = '(posts)'
        b.write_json(FB_POSTS % 1, chosen)
    if sel['facebook']['tagged_places']:
        tagged = b.load(FB_TAGGED)
        b.case = ','.join(c for k in sel['facebook']['tagged_places'] for c in case_of['facebook'][('tagged_places', json.dumps(k))])
        b.write_json(FB_TAGGED, [tagged[i] for i in sel['facebook']['tagged_places']])
    for alb in sel['facebook']['albums']:
        b.case = ','.join(case_of['facebook'][('albums', json.dumps(alb, sort_keys=True))])
        data = b.load(f"{FB_ALBUMS}/{alb['file']}")
        data['photos'] = [data['photos'][i] for i in alb['photos']]
        data.pop('cover_photo', None)
        b.write_json(f"{FB_ALBUMS}/{alb['file']}", data)
        b.copy_media_of(data['photos'])
    if sel['facebook']['uncategorized_photos']:
        data = b.load(FB_UNCAT); key = next(iter(data))
        b.case = ','.join(c for k in sel['facebook']['uncategorized_photos'] for c in case_of['facebook'][('uncategorized_photos', json.dumps(k))])
        picked = [data[key][i] for i in sel['facebook']['uncategorized_photos']]
        b.write_json(FB_UNCAT, {key: picked}); b.copy_media_of(picked)
    if sel['facebook']['videos']:
        data = b.load(FB_VIDEOS); key = next(iter(data))
        b.case = ','.join(c for k in sel['facebook']['videos'] for c in case_of['facebook'][('videos', json.dumps(k))])
        picked = [data[key][i] for i in sel['facebook']['videos']]
        b.write_json(FB_VIDEOS, {key: picked}); b.copy_media_of(picked)
    for thread, tss in msg_sel['facebook'].items():
        b.case = ','.join(case_of['facebook'][('messages', thread)])
        data = select_messages(b, FB_MESSAGES, thread, tss)
        b.write_json(f'{FB_MESSAGES}/{thread}/message_1.json', data)
        b.copy_media_of(data['messages'])

    # ---- manifest
    lines = ['# Meta testing fixture', '',
             f'Generated by `scripts/build-testing-data.py` from `{gt}` using `testdata/meta/cases.json` ({len(manifest["cases"])} cases).',
             'Do not edit by hand; edit the cases file and rebuild.', '']
    total_bytes = 0
    for b in builders.values():
        nfiles = sum(len(v) for v in b.per_case.values())
        size = sum(os.path.getsize(os.path.join(b.dst, r)) for r in b.copied)
        total_bytes += size
        lines += [f'## {b.name} -> `{b.dst}`', f'{len(b.copied)} media files ({size/1e6:.1f} MB), {nfiles - len(b.copied)} JSON files', '']
        for case, files in sorted(b.per_case.items()):
            lines.append(f'- **{case}**')
            for f in files:
                lines.append(f'  - `{f}`')
        lines.append('')
    if ig.missing or fb.missing:
        lines += ['## Missing in ground truth', ''] + [f'- {c}: `{m}`' for c, m in ig.missing + fb.missing] + ['']
    with open(os.path.join(out, 'MANIFEST.md'), 'w', encoding='utf-8') as f:
        f.write('\n'.join(lines))
    print(f'fixture written to {out}: instagram {len(ig.copied)} media files, facebook {len(fb.copied)} media files, {total_bytes/1e6:.1f} MB')
    for c, m in ig.missing + fb.missing:
        print(f'  MISSING ({c}): {m}', file=sys.stderr)
    return 1 if (ig.missing or fb.missing) else 0


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--manifest', default=MANIFEST)
    ap.add_argument('--ground-truth', default='/mnt/photos/timelinize/ground-truth')
    ap.add_argument('--out', default='/mnt/photos/timelinize/testing-data')
    a = ap.parse_args()
    with open(a.manifest, encoding='utf-8') as f:
        manifest = json.load(f)
    os.makedirs(a.out, exist_ok=True)
    sys.exit(build(manifest, a.ground_truth, a.out))


if __name__ == '__main__':
    main()
