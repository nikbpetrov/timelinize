#!/usr/bin/env python3
"""Derive every distinct *case* in a Facebook "Download your information" (JSON) export.

The export is undocumented, so instead of guessing we fingerprint what is actually there:

  * every JSON file outside the message threads: record count and top-level shape
  * posts: (normalised title, attachment shape, data keys, tags) combinations
  * post media on disk vs. the manifests that reference them (posts, albums, uncategorized, videos)
  * albums
  * messages (all thread folders): thread kinds, message key sets, attachment sub-shapes,
    content patterns (system messages, placeholders, calls…), share link hosts, attachment locations
  * the separate end-to-end-encrypted export (data_messenger_e2e/)
  * the other activity families (comments, reactions, groups, events, saved items, …)

Output is markdown (stdout, or --out FILE).  Examples are given as post indices / thread dir + timestamp,
which is what testdata/meta/cases.json uses to select fixtures.

    scripts/inventory-facebook.py /mnt/photos/timelinize/ground-truth/fb --out docs/fork/facebook-inventory.md
"""
import argparse
import collections
import glob
import json
import os
import re
import sys
from urllib.parse import urlparse


# --------------------------------------------------------------------------- helpers

def fix(s):
    """Undo Facebook's mojibake (UTF-8 bytes written as Latin-1 escapes)."""
    if not isinstance(s, str):
        return s
    try:
        return s.encode('latin-1').decode('utf-8')
    except (UnicodeEncodeError, UnicodeDecodeError):
        return s


def shape(v):
    if isinstance(v, dict):
        return '{' + ','.join(sorted(v.keys()))[:120] + '}'
    if isinstance(v, list):
        return f'[{len(v)}]' + (shape(v[0]) if v else '')
    return type(v).__name__


def load(path):
    with open(path, encoding='utf-8') as f:
        return json.load(f)


def md_table(headers, rows):
    out = ['| ' + ' | '.join(headers) + ' |', '|' + '---|' * len(headers)]
    for r in rows:
        out.append('| ' + ' | '.join(str(c).replace('|', '\\|').replace('\n', ' ') for c in r) + ' |')
    return '\n'.join(out)


class Inventory:
    def __init__(self, root, me):
        self.root = root
        self.data = os.path.join(root, 'data') if os.path.isdir(os.path.join(root, 'data')) else root
        self.me = me
        self.out = []

    def p(self, s=''):
        self.out.append(s)

    def rel(self, path):
        return os.path.relpath(path, self.data)

    # ----------------------------------------------------------------------- A. files

    def section_files(self):
        self.p('## A. Every JSON file in the export (outside message threads)')
        self.p()
        self.p('`label_values` = Facebook\'s generic "settings/log" record shape (label + value/timestamp/dict), '
               'usually low-value; the named `*_v2` arrays are the real content.')
        self.p()
        rows = []
        for f in sorted(glob.glob(os.path.join(self.data, '**', '*.json'), recursive=True)):
            r = self.rel(f)
            if '/messages/' in r:
                continue
            try:
                d = load(f)
            except Exception as e:  # noqa: BLE001
                rows.append((r, 0, f'unparseable: {e}'))
                continue
            if isinstance(d, dict):
                lv = 'label_values' in d
                parts = []
                n = 0
                for k, v in d.items():
                    if isinstance(v, list):
                        n = max(n, len(v))
                    parts.append(f'{k}={shape(v)}')
                rows.append((r, n if n else ('generic' if lv else 1), ' '.join(parts)[:150]))
            else:
                lv = bool(d) and isinstance(d[0], dict) and 'label_values' in d[0]
                rows.append((r, len(d), ('generic label_values ' if lv else '') + shape(d)[:120]))
        self.p(md_table(['file', 'records', 'shape'], rows))
        self.p()

    # ----------------------------------------------------------------------- B. posts

    def norm_title(self, t):
        t = fix(t or '').strip()
        t = t.replace(self.me, 'ME')
        t = re.sub(r'\s+', ' ', t)
        t = re.sub(r'\d+ new (photos|videos)', 'N new \\1', t)
        t = re.sub(r'\d+ photos and a video', 'N photos and a video', t)
        t = re.sub(r'from \d{1,2} \w+ \d{4}', 'from DATE', t)
        t = re.sub(r'(life event from DATE|featured collection|to the group): .*$', '\\1: …', t)
        t = re.sub(r"(wrote on|added a new photo to|shared a (?:link|reel|post|photo) to) .*?'s (timeline|profile)\.$", '\\1 X\'s \\2.', t)
        if "X's" not in t:
            t = re.sub(r'(shared a (?:link|reel|post|photo|video) to|was travelling to|is feeling|was playing|was attending|recommends|posted something via|is with|was with|is at|was at|is in|was in) .*$', '\\1 X.', t)
        return t

    def section_posts(self):
        posts = []
        for i in range(1, 100):
            f = os.path.join(self.data, 'your_facebook_activity', 'posts', f'your_posts__check_ins__photos_and_videos_{i}.json')
            if not os.path.exists(f):
                break
            posts += load(f)
        self.posts = posts
        self.p(f'## B. Posts (`your_posts__check_ins__photos_and_videos_*.json`): {len(posts)} records')
        self.p()
        self.p('One row per distinct combination of **title pattern × attachment shape × data keys × tags**. '
               '`att` lists attachment groups (`media×3` = one group with three media entries). '
               '`data` keys: `post` = own text, `update_timestamp` = last edit, `backdated_timestamp` = where a life event is placed.')
        self.p()
        c = collections.Counter()
        ex = {}
        for i, post in enumerate(posts):
            att = []
            for ag in post.get('attachments', []) or []:
                keys = sorted(set(k for a in ag.get('data', []) for k in a.keys()))
                att.append('+'.join(keys) + f'×{len(ag["data"])}')
            dkeys = sorted(set(k for d in post.get('data', []) or [] for k in d.keys()))
            key = (self.norm_title(post.get('title')), ' | '.join(att) or '—', ','.join(dkeys) or '—', 'tags' if post.get('tags') else '—')
            c[key] += 1
            ex.setdefault(key, i)
        rows = [(n, ex[k], f'`{k[0]}`', k[1], k[2], k[3]) for k, n in c.most_common()]
        self.p(md_table(['#', 'example idx', 'title pattern', 'att', 'data', 'tags'], rows))
        self.p()

        # attachment-level detail
        self.p('### B.1 Attachment sub-shapes across all posts')
        self.p()
        sub = collections.Counter()
        for post in posts:
            for ag in post.get('attachments', []) or []:
                for a in ag.get('data', []):
                    for k, v in a.items():
                        if isinstance(v, dict):
                            sub[(k, ','.join(sorted(v.keys())))] += 1
                        else:
                            sub[(k, type(v).__name__)] += 1
        self.p(md_table(['#', 'attachment', 'keys'], [(n, k[0], k[1]) for k, n in sub.most_common()]))
        self.p()
        ext = collections.Counter()
        for post in posts:
            for ag in post.get('attachments', []) or []:
                for a in ag.get('data', []):
                    ec = a.get('external_context')
                    if ec is not None:
                        u = ec.get('url', '')
                        ext[(urlparse(u).netloc or '(empty url)', 'named' if ec.get('name') else 'nameless')] += 1
        self.p('`external_context` hosts (shared links):')
        self.p()
        self.p(md_table(['#', 'host', 'name?'], [(n, k[0], k[1]) for k, n in ext.most_common(25)]))
        self.p()

    # ----------------------------------------------------------------------- C. post media

    def section_post_media(self):
        base = os.path.join(self.data, 'your_facebook_activity', 'posts')
        ref = collections.defaultdict(set)
        for i, post in enumerate(self.posts):
            for ag in post.get('attachments', []) or []:
                for a in ag.get('data', []):
                    if 'media' in a:
                        ref[a['media']['uri']].add('post')
                    for ph in a.get('life_event', {}).get('photos', []) or []:
                        ref[ph['uri']].add('life_event')
        albums = []
        for f in sorted(glob.glob(os.path.join(base, 'album', '*.json'))):
            d = load(f)
            albums.append((os.path.basename(f), d))
            for ph in d.get('photos', []):
                ref[ph['uri']].add('album')
            if d.get('cover_photo', {}).get('uri'):
                ref[d['cover_photo']['uri']].add('album_cover')
        unc = os.path.join(base, 'your_uncategorized_photos.json')
        if os.path.exists(unc):
            for ph in load(unc).get('other_photos_v2', []):
                ref[ph['uri']].add('uncategorized')
        vid = os.path.join(base, 'your_videos.json')
        if os.path.exists(vid):
            for v in load(vid).get('videos_v2', []):
                ref[v['uri']].add('your_videos')
        files = set()
        for r, _, fs in os.walk(os.path.join(base, 'media')):
            for f in fs:
                files.add(self.rel(os.path.join(r, f)))
        c = collections.Counter()
        exu = {}
        for f in sorted(files):
            k = tuple(sorted(ref.get(f, set()))) or ('UNREFERENCED',)
            c[k] += 1
            exu.setdefault(k, f)
        self.p(f'## C. Post media on disk (`posts/media/**`): {len(files)} files, referenced by which manifests')
        self.p()
        self.p(md_table(['#', 'referenced by', 'example'], [(n, ' + '.join(k), exu[k]) for k, n in c.most_common()]))
        missing = [u for u in ref if u not in files]
        self.p()
        self.p(f'Referenced but missing on disk: {len(missing)}' + (f' (e.g. {missing[:3]})' if missing else ''))
        self.p()
        sub = collections.Counter(os.path.basename(os.path.dirname(f)) for f in files)
        self.p('Folders: ' + ', '.join(f'`{k}` {v}' for k, v in sorted(sub.items(), key=lambda kv: -kv[1])))
        self.p()

        self.p('## D. Albums (`posts/album/*.json`)')
        self.p()
        rows = []
        for name, d in albums:
            kinds = collections.Counter(os.path.splitext(p['uri'])[1].lower() for p in d.get('photos', []))
            extra = sorted(set(k for p in d.get('photos', []) for k in p.keys()) - {'uri', 'creation_timestamp', 'media_metadata', 'title'})
            rows.append((name, fix(d.get('name')), len(d.get('photos', [])), ' '.join(f'{k}{v}' for k, v in kinds.items()),
                         'yes' if d.get('cover_photo', {}).get('uri') else 'no', 'yes' if fix(d.get('description')) else 'no', ','.join(extra)))
        self.p(md_table(['file', 'album', 'photos', 'types', 'cover', 'description', 'extra photo keys'], rows))
        self.p()

    # ----------------------------------------------------------------------- E. messages

    CONTENT_PATTERNS = [
        (r'^Reacted .* to your message ?$', 'pseudo-message: "Reacted X to your message"'),
        (r'^Liked a message$', 'pseudo-message: "Liked a message"'),
        (r' sent an attachment\.$', 'placeholder: "X sent an attachment."'),
        (r' sent a location\.$', 'location share: "X sent a location."'),
        (r'(missed a call|missed your call|You missed a)', 'call: missed (text)'),
        (r'(joined the (video |voice )?call|started a (video |voice )?call|The (video |voice )?call ended|started sharing video)', 'call: event text'),
        (r' named the group ', 'group: named'),
        (r' changed the group photo', 'group: photo changed'),
        (r' set the nickname | set (his|her|their|your) own nickname', 'group/thread: nickname set'),
        (r' removed .* from the group', 'group: member removed'),
        (r' added .* to the group', 'group: member added'),
        (r' left the group', 'group: member left'),
        (r' changed the chat theme| set the emoji ', 'thread: theme/emoji'),
        (r' created a poll| voted for ', 'poll'),
        (r' pinned a message', 'pinned'),
        (r'You are now connected on Messenger|Say hi to your new Facebook friend', 'system: connected'),
        (r'waved at', 'wave'),
        (r'^https?://\S+$', 'bare URL only'),
        (r'https?://', 'text containing a URL'),
        (r'^\s*$', 'empty content'),
    ]

    def section_messages(self):
        root = os.path.join(self.data, 'your_facebook_activity', 'messages')
        if not os.path.isdir(root):
            root = os.path.join(self.data, 'messages')
        self.p(f'## E. Messages (`{self.rel(root)}/`)')
        self.p()
        folders = [d for d in sorted(os.listdir(root)) if os.path.isdir(os.path.join(root, d))]
        nthreads = collections.Counter()
        nmsgs = collections.Counter()
        tkind = collections.Counter()
        keyc = collections.Counter()
        keyex = {}
        sub = collections.Counter()
        patc = collections.Counter()
        patex = {}
        hosts = collections.Counter()
        sharekind = collections.Counter()
        loc = collections.Counter()
        exists = collections.Counter()
        fext = collections.Counter()
        for folder in folders:
            tdirs = sorted(glob.glob(os.path.join(root, folder, '*/')))
            if not tdirs:
                # media-only folders (photos/, stickers_used/)
                n = sum(len(fs) for _, _, fs in os.walk(os.path.join(root, folder)))
                nthreads[folder + ' (files)'] = n
                continue
            for tdir in tdirs:
                files = sorted(glob.glob(os.path.join(tdir, 'message_*.json')), key=lambda p: int(re.findall(r'\d+', os.path.basename(p))[0]))
                if not files:
                    continue
                nthreads[folder] += 1
                tname = folder + '/' + os.path.basename(tdir.rstrip('/'))
                for fi, f in enumerate(files):
                    try:
                        d = load(f)
                    except Exception as e:  # noqa: BLE001
                        self.p(f'- unparseable: `{self.rel(f)}`: {e}')
                        continue
                    if fi == 0:
                        tkind[(folder, 'group' if len(d.get('participants', [])) > 2 else '1:1', d.get('thread_type') or '—')] += 1
                    for m in d.get('messages', []):
                        nmsgs[folder] += 1
                        ks = tuple(sorted(k for k in m.keys() if k not in ('sender_name', 'timestamp_ms', 'is_geoblocked_for_viewer', 'is_unsent_image_by_messenger_kid_parent')))
                        keyc[ks] += 1
                        keyex.setdefault(ks, (tname, m.get('timestamp_ms')))
                        for k in ('photos', 'videos', 'audio_files', 'files', 'gifs'):
                            for it in m.get(k, []) or []:
                                sub[(k, ','.join(sorted(it.keys())))] += 1
                                u = it.get('uri', '')
                                parts = u.split('/')
                                loc[(k, '/'.join(parts[:3]) if len(parts) > 3 else u)] += 1
                                exists[(k, os.path.exists(os.path.join(self.data, u)))] += 1
                                if k == 'files':
                                    fext[os.path.splitext(u)[1].lower() or '(none)'] += 1
                        if m.get('sticker'):
                            sub[('sticker', ','.join(sorted(m['sticker'].keys())))] += 1
                            u = m['sticker'].get('uri', '')
                            loc[('sticker', '/'.join(u.split('/')[:3]))] += 1
                            exists[('sticker', os.path.exists(os.path.join(self.data, u)))] += 1
                        if m.get('share') is not None:
                            s = m['share']
                            sub[('share', ','.join(sorted(s.keys())))] += 1
                            link = s.get('link', '')
                            host = urlparse(link).netloc.replace('www.', '') if link else '(no link)'
                            hosts[host] += 1
                            sharekind[self.share_kind(link)] += 1
                        c = m.get('content')
                        if c is not None:
                            c = fix(c)
                            for rx, lab in self.CONTENT_PATTERNS:
                                if re.search(rx, c):
                                    patc[lab] += 1
                                    patex.setdefault(lab, (tname, m.get('timestamp_ms')))
                                    break
        self.p('### E.1 Folders')
        self.p()
        self.p(md_table(['folder', 'threads / files', 'messages'], [(k, nthreads[k], nmsgs.get(k, '')) for k in nthreads]))
        self.p()
        self.p('### E.2 Thread kinds')
        self.p()
        self.p(md_table(['#', 'folder', 'kind', 'thread_type'], [(n, k[0], k[1], k[2]) for k, n in tkind.most_common()]))
        self.p()
        self.p('### E.3 Message key sets (ignoring `sender_name`, `timestamp_ms`, `is_geoblocked_for_viewer`, `is_unsent_image_by_messenger_kid_parent`)')
        self.p()
        rows = [(n, '`' + ('+'.join(k) or '(nothing)') + '`', f'{keyex[k][0]} @ {keyex[k][1]}') for k, n in keyc.most_common()]
        self.p(md_table(['#', 'keys', 'example (thread @ timestamp_ms)'], rows))
        self.p()
        self.p('### E.4 Attachment sub-object shapes')
        self.p()
        self.p(md_table(['#', 'field', 'keys'], [(n, k[0], k[1]) for k, n in sub.most_common()]))
        self.p()
        self.p('### E.5 Where attachment files live (uri prefix) and whether they exist in this archive')
        self.p()
        self.p(md_table(['#', 'field', 'uri prefix'], [(n, k[0], k[1]) for k, n in loc.most_common()]))
        self.p()
        self.p(md_table(['field', 'exists', '#'], [(k[0], k[1], n) for k, n in sorted(exists.items())]))
        self.p()
        self.p('`files[]` extensions: ' + ', '.join(f'`{k}` {v}' for k, v in fext.most_common()))
        self.p()
        self.p('### E.6 Content patterns (system / pseudo messages found in `content`)')
        self.p()
        self.p(md_table(['#', 'pattern', 'example'], [(n, k, f'{patex[k][0]} @ {patex[k][1]}') for k, n in patc.most_common()]))
        self.p()
        self.p('### E.7 Shares: kind and host')
        self.p()
        self.p(md_table(['#', 'kind'], [(n, k) for k, n in sharekind.most_common()]))
        self.p()
        self.p(md_table(['#', 'host'], [(n, k) for k, n in hosts.most_common(30)]))
        self.p()

    @staticmethod
    def share_kind(link):
        if not link:
            return 'no link (share_text only)'
        u = urlparse(link)
        host = u.netloc.replace('www.', '').replace('m.', '', 1) if u.netloc.startswith('m.') else u.netloc.replace('www.', '')
        path = u.path
        if host in ('instagram.com',):
            if '/reel/' in path or '/reels/' in path:
                return 'instagram reel'
            if '/p/' in path:
                return 'instagram post'
            if '/stories/' in path:
                return 'instagram story'
            return 'instagram other'
        if host.endswith('facebook.com') or host == 'fb.watch' or host == 'fb.me':
            if '/reel/' in path or host == 'fb.watch':
                return 'facebook reel/watch'
            if '/groups/' in path:
                return 'facebook group post'
            if '/events/' in path:
                return 'facebook event'
            if '/photo' in path or '/photos/' in path:
                return 'facebook photo'
            if '/videos/' in path:
                return 'facebook video'
            if '/stories/' in path:
                return 'facebook story'
            if '/marketplace/' in path:
                return 'facebook marketplace'
            if '/share/' in path:
                return 'facebook share link'
            if path.count('/') <= 1 and path.strip('/'):
                return 'facebook profile/page'
            return 'facebook other'
        if host.endswith('fbcdn.net'):
            return 'raw fbcdn media URL'
        if host in ('youtube.com', 'youtu.be'):
            return 'youtube'
        if host.endswith('9gag.com'):
            return '9gag'
        if host.endswith('giphy.com') or host.endswith('tenor.com'):
            return 'gif service'
        return 'external'

    # ----------------------------------------------------------------------- F. e2ee export

    def section_e2ee(self):
        d = os.path.join(self.root, 'data_messenger_e2e')
        self.p('## F. End-to-end encrypted export (`data_messenger_e2e/`)')
        self.p()
        if not os.path.isdir(d):
            self.p('not present')
            self.p()
            return
        files = sorted(glob.glob(os.path.join(d, '*.json')))
        keyc = collections.Counter()
        types = collections.Counter()
        med = collections.Counter()
        n = empty = 0
        top = collections.Counter()
        for f in files:
            j = load(f)
            top[','.join(sorted(j.keys()))] += 1
            if not j.get('messages'):
                empty += 1
            for m in j.get('messages', []):
                n += 1
                keyc[tuple(sorted(k for k in m if k not in ('senderName', 'timestamp')))] += 1
                types[m.get('type')] += 1
                for md in m.get('media', []) or []:
                    med[(os.path.splitext(md.get('uri', ''))[1].lower(), os.path.exists(os.path.join(d, md.get('uri', ''))) or os.path.exists(os.path.join(d, 'media', os.path.basename(md.get('uri', '')))))] += 1
        nmedia = sum(len(fs) for _, _, fs in os.walk(os.path.join(d, 'media')))
        self.p(f'{len(files)} thread files ({empty} empty), {n} messages, {nmedia} files under `media/`. '
               'camelCase schema, different from the main export: ' + '; '.join(f'`{k}`' for k in top))
        self.p()
        self.p(md_table(['#', 'message keys'], [(v, '`' + '+'.join(k) + '`') for k, v in keyc.most_common()]))
        self.p()
        self.p(md_table(['#', 'type'], [(v, k) for k, v in types.most_common()]))
        self.p()
        self.p(md_table(['#', 'media ext', 'file found'], [(v, k[0], k[1]) for k, v in med.most_common()]))
        self.p()

    # ----------------------------------------------------------------------- G. other activity

    OTHER = [
        # (relative path, top-level key or None for list, title field, description)
        ('your_facebook_activity/comments_and_reactions/comments.json', 'comments_v2', 'comments you wrote (on posts, own posts, replies); `data[].comment{comment,author,timestamp,group?}`, `attachments` for media/link comments'),
        ('your_facebook_activity/comments_and_reactions/likes_and_reactions_1.json', None, 'reactions you gave (old layout): `data[].reaction{reaction,actor}`'),
        ('your_facebook_activity/comments_and_reactions/likes_and_reactions_2.json', None, 'reactions you gave (old layout, part 2)'),
        ('your_facebook_activity/comments_and_reactions/likes_and_reactions.json', None, 'reactions you gave (new layout): label_values Reaction/URL/Name'),
        ('your_facebook_activity/groups/group_posts_and_comments.json', 'group_posts_v2', 'posts you made in groups: `data[].post`, `attachments`'),
        ('your_facebook_activity/groups/your_comments_in_groups.json', 'group_comments_v2', 'comments you made in groups'),
        ('your_facebook_activity/groups/your_group_membership_activity.json', 'groups_joined_v2', 'group joins/leaves'),
        ('your_facebook_activity/posts/posts_on_other_pages_and_profiles.json', None, 'posts you wrote on other profiles/pages (new layout, label_values Message/Target/Media)'),
        ('your_facebook_activity/events/your_events.json', 'your_events_v2', 'events you created'),
        ('your_facebook_activity/events/your_event_responses.json', 'event_responses_v2', 'events joined / interested (dict of lists)'),
        ('your_facebook_activity/events/event_invitations.json', 'events_invited_v2', 'event invitations'),
        ('your_facebook_activity/saved_items_and_collections/your_saved_items.json', 'saves_v2', 'saved links/posts/videos: `attachments[].data[].external_context{name,source,url}`'),
        ('your_facebook_activity/saved_items_and_collections/collections.json', 'collections_v2', 'saved-item collections you created'),
        ('your_facebook_activity/stories/story_reactions.json', 'stories_feedback_v2', 'reactions you sent to others\' stories'),
        ("your_facebook_activity/activity_you're_tagged_in/photos_and_videos_you're_tagged_in.json", None, 'photos/videos others tagged you in (URL + tagger name only, no media)'),
        ('your_facebook_activity/posts/places_you_have_been_tagged_in.json', None, 'tagged places (Visit time + Place name)'),
        ("your_facebook_activity/your_places/places_you've_created.json", 'your_places_v2', 'places you created'),
        ('your_facebook_activity/other_activity/pokes.json', 'pokes_v2', 'pokes'),
        ('your_facebook_activity/polls/your_poll_votes.json', None, 'poll votes'),
        ('your_facebook_activity/facebook_marketplace/items_sold.json', 'items_selling_v2', 'marketplace listings (with coordinates)'),
        ("your_facebook_activity/pages/pages_you've_liked.json", 'page_likes_v2', 'page likes'),
        ('your_facebook_activity/pages/pages_and_profiles_you_follow.json', 'pages_followed_v2', 'follows'),
        ('connections/friends/your_friends.json', 'friends_v2', 'friends (name + since)'),
        ('connections/friends/removed_friends.json', 'deleted_friends_v2', 'removed friends'),
        ('logged_information/search/your_search_history.json', 'searches_v2', 'searches'),
        ('personal_information/profile_information/profile_update_history.json', 'profile_updates_v2', 'profile changes (title only)'),
        ('personal_information/profile_information/profile_information.json', 'profile_v2', 'profile (name, birthday, emails, phones, education, places lived…)'),
        ('logged_information/notifications/notifications.json', 'notifications_v2', 'recent notifications'),
        ('security_and_login_information/account_activity.json', 'account_activity_v2', 'logins with IP/city/user-agent'),
        ('your_facebook_activity/posts/edits_you_made_to_posts.json', None, 'post edit history (text only)'),
        ('your_facebook_activity/posts/media_used_for_memories.json', None, 'media used in Memories'),
        ('your_facebook_activity/posts/shared_memories.json', None, 'memories posts you shared (media with title/description)'),
        ('your_facebook_activity/groups/your_group_messages', None, 'group chat threads (`<id>.json`, same message schema as inbox)'),
        ('files', None, 'loose files (docx/jpg) — attachments of `files[]` messages?'),
    ]

    def section_other(self):
        self.p('## G. Other activity families')
        self.p()
        rows = []
        for rel, key, desc in self.OTHER:
            path = os.path.join(self.data, rel)
            if os.path.isdir(path):
                n = sum(len(fs) for _, _, fs in os.walk(path))
                rows.append((rel, n, desc, ''))
                continue
            if not os.path.exists(path):
                rows.append((rel, '—', desc, 'absent'))
                continue
            try:
                d = load(path)
            except Exception as e:  # noqa: BLE001
                rows.append((rel, '?', desc, f'unparseable ({e})'))
                continue
            recs = d[key] if key and isinstance(d, dict) else d
            if isinstance(recs, dict):
                n = sum(len(v) if isinstance(v, list) else 1 for v in recs.values())
            else:
                n = len(recs)
            titles = collections.Counter()
            if isinstance(recs, list):
                for r in recs:
                    if isinstance(r, dict) and r.get('title'):
                        titles[self.norm_generic_title(r['title'])] += 1
            tp = '; '.join(f'`{t}` {c}' for t, c in titles.most_common(6))
            rows.append((rel, n, desc, tp))
        self.p(md_table(['file', 'records', 'what', 'title patterns'], rows))
        self.p()

    def norm_generic_title(self, t):
        t = fix(t).replace(self.me, 'ME')
        t = re.sub(r"(on|to|in|of|from|with) .+?'s (post|comment|photo|video|link|story|album|profile|timeline|event|life event|live video|reel|note)", '\\1 X\'s \\2', t)
        t = re.sub(r'(in|of|to|from|with|became a member of|posted in|replied to|reacted to|liked|saved|created a new collection:) (?!a |an |his |her |their |ME).+?(\.|$)', '\\1 X\\2', t)
        return t[:80]

    # ----------------------------------------------------------------------- run

    def run(self):
        self.p(f'# Facebook export inventory')
        self.p()
        self.p(f'Generated by `scripts/inventory-facebook.py` from `{self.root}` (owner name replaced by `ME`).')
        self.p()
        self.section_files()
        self.section_posts()
        self.section_post_media()
        self.section_messages()
        self.section_e2ee()
        self.section_other()
        return '\n'.join(self.out) + '\n'


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('root', help='export root (contains data/ and optionally data_messenger_e2e/)')
    ap.add_argument('--me', help='owner name as it appears in titles (default: from profile_information.json)')
    ap.add_argument('--out', help='write markdown here instead of stdout')
    args = ap.parse_args()
    me = args.me
    if not me:
        for cand in ('data/personal_information/profile_information/profile_information.json', 'personal_information/profile_information/profile_information.json'):
            p = os.path.join(args.root, cand)
            if os.path.exists(p):
                me = fix(load(p)['profile_v2']['name']['full_name'])
                break
    if not me:
        sys.exit('could not determine owner name; pass --me')
    md = Inventory(args.root, me).run()
    if args.out:
        with open(args.out, 'w', encoding='utf-8') as f:
            f.write(md)
        print(f'wrote {args.out} ({len(md)//1024} KB)')
    else:
        sys.stdout.write(md)


if __name__ == '__main__':
    main()
