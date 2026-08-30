#!/usr/bin/env python3
"""Verify a Meta (Instagram/Facebook) import against the export it came from.

Derives the expected item counts from the export with the fork importer's own rules
(same filters as the dev imports) and compares them with the repository. Read-only.

  scripts/verify-import.py instagram /mnt/photos/timelinize/ground-truth/ig \\
      --max-posts 3 --max-stories 5 --conversation rhys_613578166303284 \\
      --conversation stanislavrangelov_1329792411755940 --max-messages 15
  scripts/verify-import.py facebook /mnt/photos/timelinize/ground-truth/fb/data \\
      --conversation deyanakostova_1686851964727206 ... --max-messages 15 --max-posts 10
"""
import argparse, json, os, re, sqlite3, sys, glob
from urllib.parse import urlsplit, urlunsplit

PLACEHOLDER = re.compile(r'^\S.* sent an attachment\.$')
IG_EPOCH_MS = 1314220021721

def canonical(url):
    u = urlsplit(url.strip())
    host = u.netloc.lower()
    for p in ('www.', 'm.', 'mbasic.', 'l.', 'web.'):
        if host.startswith(p): host = host[len(p):]
    meta = host in ('instagram.com', 'facebook.com', 'fb.watch') or host.endswith('.facebook.com')
    query = u.query if (meta and u.path.endswith('.php')) else ''
    if not meta:
        query = '&'.join(q for q in u.query.split('&') if q and not q.startswith('utm_') and not q.startswith('fbclid'))
    path = u.path if u.path == '/' else u.path.rstrip('/')
    return urlunsplit(('https', host, path, query, ''))

def kind_of(url):
    u = urlsplit(url); segs = [s for s in u.path.split('/') if s]; first = segs[0] if segs else ''
    if u.netloc == 'instagram.com':
        return {'reel': 'reel', 'reels': 'reel', 'p': 'post', 'tv': 'post', 'stories': 'story'}.get(first, 'profile' if len(segs) == 1 else 'external')
    if u.netloc == 'fb.watch': return 'video'
    if u.netloc == 'facebook.com':
        m = {'reel': 'video', 'reels': 'video', 'watch': 'video', 'video.php': 'video', 'stories': 'story', 'groups': 'group_post',
             'events': 'event', 'photo': 'photo', 'photo.php': 'photo', 'photos': 'photo', 'permalink.php': 'post', 'story.php': 'post', 'share': 'post'}
        if first in m: return m[first]
        if len(segs) >= 2 and segs[1] in ('posts', 'photos', 'videos'): return 'video' if segs[1] == 'videos' else 'page_post'
        return 'profile' if len(segs) == 1 else 'post'
    return 'external'

def load_json(path):
    with open(path, encoding='utf-8') as f: return json.load(f)

def expected_messages(export, prefix, conversations, max_messages, owner_username, stories):
    """Mirror facebook.GetMessages: returns dict of expected counts."""
    exp = {'message': 0, 'message_no_data': 0, 'bookmark_urls': set(), 'quotes': 0, 'placeholders': 0, 'attachments': 0, 'names': set()}
    story_ts = sorted(s['creation_timestamp'] for s in stories)
    per_thread = {}
    for sub in ('inbox', 'archived_threads', 'message_requests', 'filtered_threads', 'e2ee_cutover'):
        base = os.path.join(export, prefix, sub)
        if not os.path.isdir(base): continue
        for f in sorted(glob.glob(os.path.join(base, '**', '*.json'), recursive=True)):
            thread = os.path.relpath(os.path.dirname(f), os.path.join(export, prefix))
            if conversations and not any(c in thread for c in conversations): continue
            data = load_json(f)
            for p in data.get('participants', []): exp['names'].add(p['name'])
            for m in data.get('messages', []):
                if max_messages and per_thread.get(thread, 0) >= max_messages: break
                per_thread[thread] = per_thread.get(thread, 0) + 1
                exp['names'].add(m['sender_name'])
                for r in m.get('reactions', []): exp['names'].add(r['actor'])
                text = m.get('content', '') or ''
                if PLACEHOLDER.match(text.strip()): text = ''; exp['placeholders'] += 1
                n_att = sum(len(m.get(k, [])) for k in ('photos', 'videos', 'gifs', 'audio_files')) + (1 if m.get('sticker', {}).get('uri') else 0)
                exp['attachments'] += n_att
                share = m.get('share') or {}
                has_share = bool(share.get('link') or share.get('profile_share_username'))
                quoted = False
                if has_share:
                    url = share.get('link') or ('https://www.instagram.com/%s/' % share['profile_share_username'])
                    c = canonical(url); k = kind_of(c)
                    if k == 'story':
                        mm = re.match(r'^/stories/([^/]+)/(\d+)', urlsplit(c).path)
                        if mm and mm.group(1) == owner_username and story_ts:
                            t = ((int(mm.group(2)) >> 23) + IG_EPOCH_MS) / 1000
                            import bisect
                            i = bisect.bisect_left(story_ts, t)
                            near = [story_ts[j] for j in (i - 1, i) if 0 <= j < len(story_ts)]
                            if near and min(abs(n - t) for n in near) <= 300: quoted = True
                    if quoted: exp['quotes'] += 1
                    else: exp['bookmark_urls'].add(c)
                if text.strip():
                    exp['message'] += 1 + n_att
                elif n_att:
                    exp['message'] += n_att
                elif has_share:
                    exp['message'] += 1; exp['message_no_data'] += 1
    return exp

def expected_instagram(export, a):
    pi = load_json(os.path.join(export, 'personal_information/personal_information/personal_information.json'))
    owner = pi['profile_user'][0]['string_map_data']['Username']['value']
    posts = []
    for i in range(1, 10000):
        p = os.path.join(export, 'your_instagram_activity/media/posts_%d.json' % i)
        if not os.path.exists(p): break
        posts += load_json(p)
    if a.max_posts: posts = posts[:a.max_posts]
    social = 0
    for post in posts:
        media = post.get('media', [])
        texts = [post.get('title', '')] + [m.get('title', '') for m in media]
        has_text = any(t.strip() for t in texts)
        social += (1 if has_text else 0) + len(media)
    stories = load_json(os.path.join(export, 'your_instagram_activity/media/stories.json'))['ig_stories']
    st = stories[:a.max_stories] if a.max_stories else stories
    msgs = expected_messages(export, 'your_instagram_activity/messages', a.conversation, a.max_messages, owner, stories)
    return {'social': social, 'stories': len(st), 'owner': owner, **msgs}

def expected_facebook(export, a):
    msgs = expected_messages(export, 'your_facebook_activity/messages', a.conversation, a.max_messages, '', [])
    posts = []
    for i in range(1, 10000):
        p = os.path.join(export, 'your_facebook_activity/posts/your_posts__check_ins__photos_and_videos_%d.json' % i)
        if not os.path.exists(p): break
        posts += load_json(p)
    if a.max_posts: posts = posts[:a.max_posts]
    social = location = 0
    link_keys = set()
    for post in posts:
        social += 1
        for g in post.get('attachments', []):
            for att in g.get('data', []):
                if 'media' in att: social += 1
                elif 'external_context' in att and att['external_context'].get('url'):
                    link_keys.add(canonical(att['external_context']['url']))  # bookmarks are keyed by canonical URL
                elif 'text' in att and att['text'].strip(): social += 1
    bookmarks_from_posts = len(link_keys)
    # tagged places and check-ins are imported as location items regardless of the post filter
    tagged = os.path.join(export, 'your_facebook_activity/posts/places_you_have_been_tagged_in.json')
    if os.path.exists(tagged): location += len(load_json(tagged))
    checkins = os.path.join(export, 'your_facebook_activity/posts/check-ins.json')
    if os.path.exists(checkins):
        ci = load_json(checkins); location += len(ci if isinstance(ci, list) else next(iter(ci.values()), []))
    msgs['bookmark_urls'] |= link_keys
    return {'social': social, 'location_from_posts': location, 'posts': len(posts), **msgs}

def actual(db, source):
    c = sqlite3.connect('file:%s?mode=ro' % db, uri=True)
    q = lambda sql, *p: c.execute(sql, p).fetchall()
    ds = q("select id from data_sources where name=?", source)
    if not ds: return None
    ds = ds[0][0]
    by = dict((n, cnt) for n, cnt in q("""select cl.name, count(*) from items i left join classifications cl on cl.id=i.classification_id
        where i.data_source_id=? and i.deleted is null group by 1""", ds))
    out = {'by_class': by,
           'message_no_data': q("select count(*) from items i join classifications cl on cl.id=i.classification_id where i.data_source_id=? and cl.name='message' and i.data_text is null and i.data_file is null", ds)[0][0],
           'message_with_link_text': q("select count(*) from items i join classifications cl on cl.id=i.classification_id where i.data_source_id=? and cl.name='message' and (i.data_text like '%instagram.com/%' or i.data_text like '%facebook.com/%')", ds)[0][0],
           'bookmarks': dict(q("select json_extract(metadata,'$.Status'), count(*) from items i join classifications cl on cl.id=i.classification_id where i.data_source_id=? and cl.name='bookmark' group by 1", ds)),
           'fetched_media': q("select count(*) from items where data_source_id=? and original_id like '%#%'", ds)[0][0],
           'quotes': q("select count(*) from relationships r join relations rel on rel.id=r.relation_id join items i on i.id=r.from_item_id where rel.label='quotes' and i.data_source_id=?", ds)[0][0],
           'mojibake_entities': q("select count(*) from entities where name like '%Ð%' or name like '%Ñ%'")[0][0],
           'jobs': q("select id, state, message from jobs where type='import' and configuration like ?", '%"data_source_name":"' + source + '"%'),
           'immich': q("""select count(distinct i.data_hash), count(distinct ia.asset_id), sum(ia.evicted is not null) from items i left join immich_assets ia on ia.data_hash=i.data_hash
               where i.data_source_id=? and i.data_file is not null and (i.data_type like 'image/%' or i.data_type like 'video/%')""", ds)[0]}
    return out

def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('source', choices=['instagram', 'facebook']); ap.add_argument('export')
    ap.add_argument('--repo', default='/mnt/photos/timelinize/repo-dev')
    ap.add_argument('--max-posts', type=int, default=0); ap.add_argument('--max-stories', type=int, default=0)
    ap.add_argument('--conversation', action='append', default=[]); ap.add_argument('--max-messages', type=int, default=0)
    a = ap.parse_args()
    exp = expected_instagram(a.export, a) if a.source == 'instagram' else expected_facebook(a.export, a)
    act = actual(os.path.join(a.repo, 'timeline.db'), a.source)
    if act is None: print('no items from', a.source, 'in', a.repo); sys.exit(1)
    ok = True
    def check(label, e, v):
        nonlocal ok
        good = (e == v); ok &= good
        print('  %-34s expected %-6s actual %-6s %s' % (label, e, v, 'OK' if good else 'MISMATCH'))
    print('== %s: %s vs %s' % (a.source, a.export, a.repo))
    by = act['by_class']
    check('messages', exp['message'], by.get('message', 0))
    check('messages without data (share-only)', exp['message_no_data'], act['message_no_data'])
    check('messages with a link as text', 0, act['message_with_link_text'])
    check('bookmarks (distinct share URLs)', len(exp['bookmark_urls']), sum(act['bookmarks'].values()))
    check('own-story quotes', exp['quotes'], act['quotes'])
    check('social (posts + post media)', exp['social'], by.get('social', 0))
    if a.source == 'instagram':
        media_expected = exp['stories'] + exp['quotes'] + act['fetched_media']  # quoted stories outside the story filter add media items
        check('media (stories + quoted + fetched)', media_expected, by.get('media', 0))
    else:
        check('locations (tagged places + check-ins)', exp['location_from_posts'], by.get('location', 0))
    check('mojibake entities', 0, act['mojibake_entities'])
    print('  bookmarks by status: %s; fetched media items: %d; placeholders dropped: %d' % (act['bookmarks'], act['fetched_media'], exp['placeholders']))
    files, in_immich, evicted = act['immich']
    print('  immich: %s of %s media files mapped, %s evicted locally' % (in_immich, files, evicted or 0))
    for jid, state, msg in act['jobs']: print('  job %d %s: %s' % (jid, state, msg))
    print('RESULT:', 'OK' if ok else 'MISMATCH'); sys.exit(0 if ok else 2)

if __name__ == '__main__': main()
