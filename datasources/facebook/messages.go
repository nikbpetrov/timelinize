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
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/timelinize/timelinize/internal/linkfetch"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// GetMessages imports Messenger/Instagram DM threads. filters may be
// zero-valued to import everything.
func GetMessages(ctx context.Context, dsName string, dirEntry timeline.DirEntry, params timeline.ImportParams, filters Filters, mctx MessageContext) error {
	// figure out which archive version we're working with
	messagesInboxPrefix := messagesPrefix2025
	if _, err := fs.Stat(dirEntry.FS, messagesInboxPrefix); errors.Is(err, fs.ErrNotExist) {
		messagesInboxPrefix = year2024MessagesPrefix
	}
	if _, err := fs.Stat(dirEntry.FS, messagesInboxPrefix); errors.Is(err, fs.ErrNotExist) {
		messagesInboxPrefix = pre2024MessagesPrefix
	}

	// messages imported per thread folder (threads may span several message_N.json files)
	threadMsgCounts := make(map[string]int)

	// share-link statistics for the end-of-import summary
	stats := shareStats{byKind: make(map[string]int), byMsgKind: make(map[string]int)}

	// optional link resolver (downloads shared reels/posts with the user's cookies)
	var resolver *linkfetch.Resolver
	if params.LinkFetch != nil && params.LinkFetch.Enabled {
		var err error
		resolver, err = linkfetch.New(*params.LinkFetch, params.Log.Named("link_fetch"))
		if err != nil {
			return fmt.Errorf("setting up link fetching: %w", err)
		}
		params.Log.Info("link fetching enabled",
			zap.String("cache_dir", params.LinkFetch.CacheDir),
			zap.Int("max_per_import", params.LinkFetch.MaxPerImport),
			zap.Int("delay_ms", params.LinkFetch.DelayMS),
			zap.Bool("cookies", params.LinkFetch.Cookies[dsName] != ""))
	}

	for _, messageSubfolder := range []string{
		"inbox",
		"archived_threads",
		"message_requests",
		"filtered_threads",
		"e2ee_cutover", // Messenger threads migrated to end-to-end encryption (2024+)
	} {
		err := fs.WalkDir(dirEntry.FS, path.Join(messagesInboxPrefix, messageSubfolder), func(fpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if path.Ext(d.Name()) != ".json" {
				return nil
			}

			// thread folder relative to the messages folder, e.g. "inbox/somebody_123"
			threadPath, _ := strings.CutPrefix(path.Dir(fpath), messagesInboxPrefix+"/")
			if !filters.wantThread(threadPath) {
				return nil
			}

			file, err := dirEntry.FS.Open(fpath)
			if err != nil {
				return err
			}
			defer file.Close()

			var thread fbMessengerThread
			if err := json.NewDecoder(file).Decode(&thread); err != nil {
				return err
			}
			threadMeta := thread.metadata(dsName, threadPath, mctx.OwnerName)
			pseudo := collectPseudoReactions(thread.Messages)

			for _, msg := range thread.Messages {
				if filters.MaxMessagesPerConversation > 0 && threadMsgCounts[threadPath] >= filters.MaxMessagesPerConversation {
					break
				}
				threadMsgCounts[threadPath]++

				if err := ctx.Err(); err != nil {
					return err
				}
				if params.Continue != nil {
					if err := params.Continue(); err != nil {
						return err
					}
				}

				senderName := FixString(msg.SenderName)
				sender := participantEntity(dsName, senderName, threadPath)
				msgText := FixString(msg.Content)
				msgTimestamp := time.UnixMilli(msg.TimestampMS).UTC()

				// what is this message? (call, group event, placeholder, link, ... — classify.go)
				cls := classifyMessage(msg, msgText)
				stats.byMsgKind[string(cls.Kind)]++
				if cls.dropped() {
					continue
				}
				if cls.Kind == kindPlaceholder {
					// Meta substitutes "<Name> sent an attachment." for messages whose real
					// content is the attachment or share; that is not something the sender typed
					msgText = ""
					stats.placeholdersDropped++
				}
				if cls.Kind == kindLink && msg.Share.isEmpty() {
					// a message that is exactly one URL: the text stays, and the URL gets the
					// same bookmark treatment as a share
					msg.Share.Link = cls.URL
				}

				attachments, expiredLinks, missing := messageAttachments(msg, sender, dirEntry, msgTimestamp, params.Log)

				// A shared post/reel/story/profile becomes its own bookmark item attached to the
				// message (or, for the owner's own story, a "quotes" edge to the imported story),
				// never text on the message: share_text is the *other* author's caption.
				var bookmark, quoted *timeline.Item
				var fetchedMedia []*timeline.Item
				if !msg.Share.isEmpty() && cls.Kind != kindLocation {
					canonical := canonicalShareURL(msg.Share.url(dsName))
					kind := classifyShareURL(canonical)
					status := shareStatusUnresolved
					if kind == shareKindStory {
						status = shareStatusExpired // stories live 24 h; only the owner's own are in the export
						if user, mediaID, ok := storyLink(canonical); ok && mctx.OwnStory != nil &&
							mctx.OwnerUsername != "" && user == mctx.OwnerUsername {
							quoted = mctx.OwnStory(instagramMediaIDTime(mediaID))
						}
					}
					if quoted != nil {
						stats.ownStoriesMatched++
					} else {
						if status != shareStatusExpired {
							// kinds that are never fetched are terminal right away, resolver or not
							if backend, terminal := linkfetch.Route(linkfetch.Request{URL: canonical, Kind: kind, Site: dsName}); backend == linkfetch.BackendNone && terminal != "" {
								status = terminal
							}
						}
						bookmark = msg.Share.bookmarkItem(dsName, canonical, kind, status, msgTimestamp)
						if resolver != nil && status == shareStatusUnresolved {
							fetched, err := resolveShare(ctx, resolver, dsName, canonical, kind, msgTimestamp)
							if err != nil {
								return err
							}
							bookmark.Metadata["Status"] = fetched.Status
							if fetched.Status == linkfetch.StatusResolved {
								bookmark.Metadata["Fetched with"] = fetched.Backend
							}
							if fetched.Error != "" {
								bookmark.Metadata["Fetch error"] = fetched.Error
							}
							status = fetched.Status
							fetchedMedia = fetched.Items
						}
					}
					stats.byKind[kind]++
					params.Log.Debug("share link in message",
						zap.String("thread", threadPath),
						zap.Time("timestamp", msgTimestamp),
						zap.String("url", canonical),
						zap.String("kind", kind),
						zap.String("status", status),
						zap.Bool("own_story_matched", quoted != nil))
				}

				// keep only what the sender actually typed (a message may legitimately
				// contain the link in its text too; that is the sender's, so it stays)
				msgText = strings.TrimSpace(msgText)

				var item *timeline.Item
				switch {
				case msgText != "":
					item = &timeline.Item{
						Classification: timeline.ClassMessage,
						Timestamp:      msgTimestamp,
						Owner:          sender,
						Content: timeline.ItemData{
							Data: timeline.StringData(msgText),
						},
					}
				case len(attachments) > 0:
					item, attachments = attachments[0], attachments[1:]
				case bookmark != nil || quoted != nil || len(expiredLinks) > 0:
					// the message consists solely of a share: represent it as an empty message
					// (kept by the pipeline because it has relationships) so it still shows up
					// in the conversation at the right time, owned by the sender
					item = &timeline.Item{
						Classification: timeline.ClassMessage,
						Timestamp:      msgTimestamp,
						Owner:          sender,
					}
				default:
					// found an empty message; I've seen this happen rarely,
					// like if a message IsUnsent; no content, so skip
					continue
				}

				if item.Metadata == nil {
					item.Metadata = make(timeline.Metadata)
				}
				for k, v := range threadMeta {
					item.Metadata[k] = v
				}
				cls.annotate(item, msgTimestamp)
				if msg.IP != "" {
					item.Metadata["IP"] = msg.IP
				}
				if missing > 0 {
					item.Metadata["Missing attachments"] = missing
				}

				ig := &timeline.Graph{Item: item}

				for _, attach := range attachments {
					ig.ToItem(timeline.RelAttachment, attach)
				}
				for _, expired := range expiredLinks {
					ig.ToItem(timeline.RelAttachment, expired)
				}
				if bookmark != nil {
					bg := &timeline.Graph{Item: bookmark}
					for _, media := range fetchedMedia {
						bg.ToItem(timeline.RelAttachment, media)
					}
					ig.Edges = append(ig.Edges, timeline.Relationship{Relation: timeline.RelAttachment, To: bg})
				}
				if quoted != nil {
					ig.ToItem(timeline.RelQuotes, quoted)
				}
				for _, recipient := range thread.sentTo(sender, dsName, threadPath) {
					ig.ToEntity(timeline.RelSent, recipient)
				}
				for _, reaction := range msg.Reactions {
					actor := participantEntity(dsName, FixString(reaction.Actor), threadPath)
					ig.FromEntityWithValue(&actor, timeline.RelReacted, FixString(reaction.Reaction))
					// the export also emits a "Reacted X to your message" pseudo-message from the
					// actor (dropped above); its timestamp is when the reaction happened
					if t, ok := pseudo.timeOf(FixString(reaction.Actor), msg.TimestampMS); ok {
						ig.Edges[len(ig.Edges)-1].Start = &t
					}
				}

				params.Pipeline <- ig
			}

			return nil
		})
		// since we try different folders above, ignore NotExist since some just might not exist in the archive
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("walking messages: %w", err)
		}
	}

	summary := []zap.Field{
		zap.String("data_source", dsName),
		zap.Any("by_message_kind", stats.byMsgKind),
		zap.Any("by_kind", stats.byKind),
		zap.Int("placeholders_dropped", stats.placeholdersDropped),
		zap.Int("own_stories_matched", stats.ownStoriesMatched),
	}
	if resolver != nil {
		summary = append(summary, zap.Any("link_fetch", resolver.Stats()))
	}
	params.Log.Info("messages: share links summary", summary...)

	return nil
}

type shareStats struct {
	byMsgKind           map[string]int
	byKind              map[string]int
	placeholdersDropped int
	ownStoriesMatched   int
}

// messageAttachments builds the attachment items of a message: photos, videos, gifs, audio,
// files (documents) and the sticker. Attachments whose file is not in the archive are not
// items: a bare filename (2017 threads) is counted as missing, an https CDN URL (expired)
// becomes a bookmark with Status expired so the URL survives.
func messageAttachments(msg fbMessage, sender timeline.Entity, dirEntry timeline.DirEntry, msgTimestamp time.Time, logger *zap.Logger) (attachments, expired []*timeline.Item, missing int) {
	add := func(media fbArchiveMedia, class timeline.Classification) {
		if media.URI == "" || !strings.Contains(media.URI, "/") {
			missing++ // 2017 threads: bare filename or empty uri, the file is not in the archive
			return
		}
		if strings.HasPrefix(media.URI, "http://") || strings.HasPrefix(media.URI, "https://") {
			expired = append(expired, &timeline.Item{
				ID:             media.URI,
				Classification: timeline.ClassBookmark,
				Timestamp:      msgTimestamp,
				Owner:          sender,
				Content:        timeline.ItemData{Data: timeline.StringData(media.URI)},
				Metadata: timeline.Metadata{
					"URL":    media.URI,
					"Kind":   "media",
					"Status": shareStatusExpired,
				},
			})
			return
		}
		if info, err := fs.Stat(dirEntry.FS, media.URI); err != nil || info.IsDir() {
			missing++
			return
		}
		attached := &timeline.Item{
			Classification: class,
			Owner:          sender,
		}
		media.fillItem(attached, dirEntry, "", logger)
		if attached.Timestamp.IsZero() {
			attached.Timestamp = msgTimestamp
		}
		attachments = append(attachments, attached)
	}
	for _, m := range msg.Photos {
		add(m, timeline.ClassMessage)
	}
	for _, m := range msg.Videos {
		add(m, timeline.ClassMessage)
	}
	for _, m := range msg.GIFs {
		add(m, timeline.ClassMessage)
	}
	for _, m := range msg.AudioFiles {
		add(m, timeline.ClassMessage)
	}
	for _, m := range msg.Files {
		add(m, timeline.ClassDocument)
	}
	if msg.Sticker.URI != "" {
		add(msg.Sticker, timeline.ClassMessage)
	}
	return attachments, expired, missing
}

// annotate writes what the classifier found onto the message item.
func (c classified) annotate(item *timeline.Item, msgTimestamp time.Time) {
	switch c.Kind {
	case kindCall:
		item.Metadata["Kind"] = "call"
		item.Metadata["Direction"] = c.Direction
		item.Metadata["Duration"] = c.Duration
		item.Metadata["Missed"] = c.Missed
		item.Metadata["Video"] = c.Video
		if c.Duration > 0 {
			item.Timespan = msgTimestamp.Add(time.Duration(c.Duration) * time.Second)
		}
	case kindGroupEvent, kindCallEvent:
		item.Metadata["Kind"] = "system"
		item.Metadata["Event"] = c.Event
		if c.Subject != "" {
			item.Metadata["Subject"] = c.Subject
		}
	case kindLocation:
		item.Metadata["Kind"] = "location"
		if c.Latitude != nil && c.Longitude != nil {
			item.Location.Latitude = c.Latitude
			item.Location.Longitude = c.Longitude
		}
		if c.Address != "" {
			item.Metadata["Address"] = c.Address
		}
	}
}

// pseudoReactions remembers, per actor, when the export's "Reacted X to your message"
// pseudo-messages were sent, so the real reaction edge can carry that time.
type pseudoReactions map[string][]int64

func collectPseudoReactions(msgs []fbMessage) pseudoReactions {
	p := make(pseudoReactions)
	for _, m := range msgs {
		if pseudoReactionRegex.MatchString(strings.TrimSpace(FixString(m.Content))) {
			actor := FixString(m.SenderName)
			p[actor] = append(p[actor], m.TimestampMS)
		}
	}
	return p
}

// timeOf returns the earliest pseudo-reaction by actor after a message sent at ts.
func (p pseudoReactions) timeOf(actor string, ts int64) (time.Time, bool) {
	var best int64
	for _, t := range p[actor] {
		if t > ts && (best == 0 || t < best) {
			best = t
		}
	}
	if best == 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(best).UTC(), true
}
