/*
	Timelinize
	Copyright (c) 2013 Matthew Holt

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published
	by the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package facebook

import (
	"context"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/timelinize/timelinize/internal/linkfetch"
	"github.com/timelinize/timelinize/timeline"
)

// fbShare is the "share" object of a Messenger/Instagram DM: a link to a post, reel,
// story, profile, event, etc. that was forwarded into the conversation. Meta puts the
// *other* author's caption in share_text, so it must never be treated as the sender's words.
type fbShare struct {
	Link                 string `json:"link"`
	ShareText            string `json:"share_text"`
	OriginalContentOwner string `json:"original_content_owner"`
	ProfileShareUsername string `json:"profile_share_username"`
	ProfileShareName     string `json:"profile_share_name"`
}

func (s fbShare) isEmpty() bool { return s.Link == "" && s.ProfileShareUsername == "" }

// url returns the URL of the shared thing (profile shares carry a username instead of a link).
func (s fbShare) url(dsName string) string {
	if s.Link != "" {
		return s.Link
	}
	if dsName == "instagram" {
		return "https://www.instagram.com/" + s.ProfileShareUsername + "/"
	}
	return "https://www.facebook.com/" + s.ProfileShareUsername
}

// MessageContext carries per-import context that message processing needs from the
// data source (Instagram or Facebook) that owns the archive.
type MessageContext struct {
	// The archive owner's username on this platform (e.g. Instagram handle),
	// used to recognize links to the owner's own content.
	OwnerUsername string

	// The archive owner's display name as it appears in sender_name/participants,
	// used to tell the counterpart of a 1:1 thread from the owner.
	OwnerName string

	// OwnStory, if set, returns an item representing the owner's story that was
	// published closest to t (within a tolerance), or nil if there is none. Used to
	// link DMs that share the owner's own story to the imported story item instead
	// of creating a bookmark for an expired URL.
	OwnStory func(t time.Time) *timeline.Item
}

// Share kinds (stored in bookmark metadata "Kind") — see docs/fork/link-fetching.md.
const (
	shareKindReel      = "reel"
	shareKindPost      = "post"
	shareKindStory     = "story"
	shareKindProfile   = "profile"
	shareKindVideo     = "video"
	shareKindGroupPost = "group_post"
	shareKindEvent     = "event"
	shareKindPagePost  = "page_post"
	shareKindPhoto     = "photo"
	shareKindExternal  = "external"
)

// Resolve statuses (bookmark metadata "Status"). "unresolved" bookmarks are
// candidates for the link resolver; the others are terminal.
const (
	shareStatusUnresolved = "unresolved"
	shareStatusExpired    = "expired" // e.g. someone else's story
)

var attachmentPlaceholderRegex = regexp.MustCompile(`^\S.* sent an attachment\.$`)

// isAttachmentPlaceholder reports whether text is Meta's auto-generated stand-in for a
// message whose real content is the attachment/share ("You sent an attachment.",
// "<Name> sent an attachment.").
func isAttachmentPlaceholder(text string) bool {
	return attachmentPlaceholderRegex.MatchString(strings.TrimSpace(text))
}

// tracking/query parameters that never identify content
var junkQueryParams = map[string]bool{
	"igsh": true, "igshid": true, "carousel_share_child_media_id": true, "mibextid": true,
	"rdid": true, "share_url": true, "fbclid": true, "ref": true, "refid": true, "_rdr": true,
	"utm_source": true, "utm_medium": true, "utm_campaign": true, "utm_content": true, "utm_term": true,
	"id": true, // the "?id=<share id>" Meta appends to instagram links (not the story.php one; handled below)
}

// canonicalShareURL normalizes a shared link so the same content yields the same key
// across exports and senders: lower-case host without "m."/"www."/"l." mobile/link
// prefixes, tracking params dropped, fragment dropped, trailing slash normalized.
func canonicalShareURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	u.Scheme = "https"
	u.Fragment = ""
	host := strings.ToLower(u.Host)
	for _, p := range []string{"www.", "m.", "mbasic.", "l.", "web."} {
		host = strings.TrimPrefix(host, p)
	}
	u.Host = host

	isMeta := host == "instagram.com" || host == "facebook.com" || host == "fb.watch" || strings.HasSuffix(host, ".facebook.com")
	q := u.Query()
	if isMeta {
		keepQuery := strings.HasSuffix(u.Path, ".php") // permalink.php?story_fbid=..&id=.. etc. need their params
		for k := range q {
			if !keepQuery || (junkQueryParams[k] && k != "id") {
				q.Del(k)
			}
		}
	} else {
		for k := range q {
			if strings.HasPrefix(k, "utm_") || k == "fbclid" {
				q.Del(k)
			}
		}
	}
	u.RawQuery = q.Encode()
	if u.Path != "/" {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}
	return u.String()
}

// classifyShareURL returns the kind of content a (canonical) URL points to.
func classifyShareURL(canonical string) string {
	u, err := url.Parse(canonical)
	if err != nil {
		return shareKindExternal
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	first := ""
	if len(segs) > 0 {
		first = segs[0]
	}
	switch u.Host {
	case "instagram.com":
		switch first {
		case "reel", "reels":
			return shareKindReel
		case "p", "tv":
			return shareKindPost
		case "stories":
			return shareKindStory
		default:
			if len(segs) == 1 && first != "" {
				return shareKindProfile
			}
			return shareKindExternal
		}
	case "fb.watch":
		return shareKindVideo
	case "facebook.com":
		switch first {
		case "reel", "reels", "watch", "video.php":
			return shareKindVideo
		case "stories":
			return shareKindStory
		case "groups":
			return shareKindGroupPost
		case "events":
			return shareKindEvent
		case "photo", "photo.php", "photos":
			return shareKindPhoto
		case "permalink.php", "story.php", "share":
			return shareKindPost
		case "":
			return shareKindExternal
		}
		if len(segs) >= 2 {
			switch segs[1] {
			case "posts", "photos", "videos":
				if segs[1] == "videos" {
					return shareKindVideo
				}
				return shareKindPagePost
			}
		}
		if len(segs) == 1 {
			return shareKindProfile
		}
		return shareKindPost
	}
	return shareKindExternal
}

var storyPathRegex = regexp.MustCompile(`^/stories/([^/]+)/(\d+)`)

// storyLink extracts the username and media ID from an Instagram story URL.
func storyLink(canonical string) (username string, mediaID int64, ok bool) {
	u, err := url.Parse(canonical)
	if err != nil || u.Host != "instagram.com" {
		return "", 0, false
	}
	m := storyPathRegex.FindStringSubmatch(u.Path)
	if m == nil {
		return "", 0, false
	}
	id, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return m[1], id, true
}

// Instagram media IDs are Snowflake-style: the upper bits are milliseconds since
// Instagram's epoch (2011-08-24T21:07:01.721Z). Verified against this archive's
// stories.json: decoded time precedes the story's creation_timestamp by 1-90 s.
const instagramEpochMillis = 1314220021721

func instagramMediaIDTime(id int64) time.Time {
	const timestampShift = 23
	return time.UnixMilli((id >> timestampShift) + instagramEpochMillis).UTC()
}

// bookmarkItem builds the item that represents a shared link inside a message.
// It is keyed by the canonical URL so the same content shared twice (or in two
// exports with different captions) is one item. Ownership is left empty on purpose:
// the sharer owns the *message*; the original author is recorded in metadata rather
// than as an entity, to avoid creating an entity per random creator.
func (s fbShare) bookmarkItem(dsName string, canonical, kind, status string, ts time.Time) *timeline.Item {
	meta := timeline.Metadata{
		"URL":    canonical,
		"Kind":   kind,
		"Status": status,
	}
	if s.Link != "" && s.Link != canonical {
		meta["Original URL"] = s.Link
	}
	if author := FixString(s.OriginalContentOwner); author != "" {
		meta["Author"] = author
	} else if s.ProfileShareUsername != "" {
		meta["Author"] = s.ProfileShareUsername
	}
	if caption := strings.TrimSpace(FixString(s.ShareText)); caption != "" {
		meta["Caption"] = caption
	}
	if s.ProfileShareName != "" {
		meta["Title"] = FixString(s.ProfileShareName)
	}
	return &timeline.Item{
		ID:             canonical,
		Classification: timeline.ClassBookmark,
		Timestamp:      ts,
		Content: timeline.ItemData{
			Data: timeline.StringData(canonical),
		},
		Metadata: meta,
	}
}

// fetchedShare is what the link resolver produced for a share: a status for the
// bookmark and media items (one per downloaded file/slide) to attach to it.
type fetchedShare struct {
	Status  string
	Backend string
	Error   string
	Items   []*timeline.Item
}

// resolveShare runs the link resolver for a shared URL and turns downloaded files
// into media items. The items are keyed "<canonical url>#<slide>" so re-imports match
// them by original ID, and their data is read from the resolver's cache.
func resolveShare(ctx context.Context, resolver *linkfetch.Resolver, dsName, canonical, kind string, fallbackTS time.Time) (fetchedShare, error) {
	res, err := resolver.Resolve(ctx, linkfetch.Request{URL: canonical, Kind: kind, Site: dsName})
	if err != nil {
		return fetchedShare{}, err
	}
	out := fetchedShare{Status: res.Status, Backend: res.Backend, Error: res.Error}
	for _, f := range res.Files {
		path := f.Path
		it := &timeline.Item{
			ID:                   canonical + "#" + strconv.Itoa(f.Index),
			Classification:       timeline.ClassMedia,
			Timestamp:            fallbackTS,
			IntermediateLocation: canonical,
			Content: timeline.ItemData{
				Filename: f.Name,
				Size:     uint64(f.Size), //nolint:gosec // sizes are non-negative
				Data: func(_ context.Context) (io.ReadCloser, error) {
					return os.Open(path)
				},
			},
			Metadata: make(timeline.Metadata, len(f.Metadata)+2),
		}
		for k, v := range f.Metadata {
			it.Metadata[k] = v
		}
		if published, ok := f.Metadata[linkfetch.MetaPublished].(time.Time); ok && !published.IsZero() {
			it.Timestamp = published
		}
		it.Metadata["URL"] = canonical
		out.Items = append(out.Items, it)
	}
	return out, nil
}
