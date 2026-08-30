package facebook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// The end-to-end-encrypted Messenger export is a separate download ("Download your
// end-to-end encrypted chats" in Messenger) with its own layout: one <Name>_<n>.json per
// 1:1 thread at the root, media under media/<uuid>.<ext>, camelCase fields. It continues
// the main export's e2ee_cutover threads (no overlap). See docs/fork/messenger-plan.md §4.
//
//	{"participants": ["Me", "Them"], "threadName": "Them_40",
//	 "messages": [{"senderName": "Me", "timestamp": 1746073791906, "text": "…",
//	               "type": "text|media|link|placeholder", "media": [{"uri": "./media/<uuid>.jpeg"}],
//	               "reactions": [{"actor": "Them", "reaction": "❤"}], "isUnsent": false}]}

type e2eeThread struct {
	Participants []string      `json:"participants"`
	ThreadName   string        `json:"threadName"`
	Messages     []e2eeMessage `json:"messages"`
}

type e2eeMessage struct {
	SenderName string `json:"senderName"`
	Timestamp  int64  `json:"timestamp"` // ms
	Text       string `json:"text"`
	Type       string `json:"type"` // text, media, link, placeholder
	IsUnsent   bool   `json:"isUnsent"`
	Media      []struct {
		URI string `json:"uri"` // "./media/<uuid>.<ext>", or the literal "Failed to download media"
	} `json:"media"`
	Reactions []struct {
		Actor    string `json:"actor"`
		Reaction string `json:"reaction"`
	} `json:"reactions"`
}

const e2eeMediaDir = "media"

var e2eeThreadSuffix = regexp.MustCompile(`_\d+$`)

// isE2EEExport reports whether the import root is an E2EE Messenger export: JSON thread
// files at the root carrying a threadName, no your_facebook_activity/ folder.
func isE2EEExport(dirEntry timeline.DirEntry) bool {
	if dirEntry.FileExists("your_facebook_activity") || dirEntry.FileExists(year2024ProfileInfoPath) {
		return false
	}
	entries, err := fs.ReadDir(dirEntry.FS, ".")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".json" {
			continue
		}
		f, err := dirEntry.FS.Open(e.Name())
		if err != nil {
			continue
		}
		var head struct {
			ThreadName string `json:"threadName"`
		}
		err = json.NewDecoder(f).Decode(&head)
		f.Close()
		if err == nil && head.ThreadName != "" {
			return true
		}
	}
	return false
}

// importE2EE walks an E2EE export and feeds every thread through the same message
// processing as the main export.
func (a *Archive) importE2EE(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams, dsOpt *Options) error {
	entries, err := fs.ReadDir(dirEntry.FS, ".")
	if err != nil {
		return err
	}
	var threads []e2eeThread
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".json" {
			continue
		}
		f, err := dirEntry.FS.Open(e.Name())
		if err != nil {
			return err
		}
		var t e2eeThread
		err = json.NewDecoder(f).Decode(&t)
		f.Close()
		if err != nil {
			return fmt.Errorf("decoding E2EE thread %s: %w", e.Name(), err)
		}
		if t.ThreadName == "" {
			t.ThreadName = strings.TrimSuffix(e.Name(), ".json")
		}
		threads = append(threads, t)
	}
	if len(threads) == 0 {
		return errors.New("E2EE export contains no threads")
	}

	// The export never says who the owner is; it is the one name present in every thread.
	ownerName := e2eeOwnerName(threads)
	if ownerName == "" {
		if repoOwner, ok := ctx.Value(timeline.RepoOwnerCtxKey).(timeline.Entity); ok {
			ownerName = repoOwner.Name
		}
	}
	if ownerName == "" {
		return errors.New("cannot tell the archive owner from the E2EE threads; set the repository owner first")
	}
	params.Log.Info("E2EE Messenger export", zap.Int("threads", len(threads)), zap.String("owner", ownerName))

	w, err := newMessageWalker(ctx, "facebook", dirEntry, params, dsOpt.Filters, MessageContext{OwnerUsername: dsOpt.Username, OwnerName: ownerName})
	if err != nil {
		return err
	}
	for _, t := range threads {
		threadPath := "e2ee/" + t.ThreadName
		if !dsOpt.Filters.wantThread(threadPath) {
			continue
		}
		if err := w.processThread(t.toThread(), threadPath); err != nil {
			return err
		}
	}
	w.summary()
	return nil
}

// e2eeOwnerName returns the single participant present in every thread, or "".
func e2eeOwnerName(threads []e2eeThread) string {
	counts := make(map[string]int)
	for _, t := range threads {
		seen := make(map[string]bool)
		for _, p := range t.Participants {
			if !seen[p] {
				seen[p] = true
				counts[p]++
			}
		}
	}
	var owner string
	for name, n := range counts {
		if n == len(threads) {
			if owner != "" {
				return "" // ambiguous (a single 1:1 thread)
			}
			owner = name
		}
	}
	return owner
}

// toThread converts an E2EE thread into the main export's shape so processThread can
// handle it: media are sorted into photos/videos/audio/gifs/files by extension, the
// "./media/" prefix is made archive-relative, placeholders become unsent messages.
func (t e2eeThread) toThread() fbMessengerThread {
	var out fbMessengerThread
	for _, p := range t.Participants {
		out.Participants = append(out.Participants, struct {
			Name string `json:"name"`
		}{Name: p})
	}
	out.Title = e2eeThreadSuffix.ReplaceAllString(t.ThreadName, "")
	out.IsStillParticipant = true
	for _, m := range t.Messages {
		msg := fbMessage{
			SenderName:  m.SenderName,
			TimestampMS: m.Timestamp,
			Content:     m.Text,
			IsUnsent:    m.IsUnsent || m.Type == "placeholder",
		}
		for _, r := range m.Reactions {
			msg.Reactions = append(msg.Reactions, struct {
				Reaction string `json:"reaction"`
				Actor    string `json:"actor"`
			}{Reaction: r.Reaction, Actor: r.Actor})
		}
		for _, md := range m.Media {
			uri := strings.TrimPrefix(md.URI, "./")
			if !strings.Contains(uri, "/") {
				// "Failed to download media": no file; counted as a missing attachment
				msg.Photos = append(msg.Photos, fbArchiveMedia{URI: ""})
				continue
			}
			media := fbArchiveMedia{URI: uri}
			switch strings.ToLower(path.Ext(uri)) {
			case ".jpg", ".jpeg", ".png", ".webp", ".heic":
				msg.Photos = append(msg.Photos, media)
			case ".gif":
				msg.GIFs = append(msg.GIFs, media)
			case ".mp4", ".mov", ".webm":
				msg.Videos = append(msg.Videos, media)
			case ".ogg", ".oga", ".opus", ".aac", ".m4a", ".mp3", ".wav":
				msg.AudioFiles = append(msg.AudioFiles, media)
			default:
				msg.Files = append(msg.Files, media)
			}
		}
		out.Messages = append(out.Messages, msg)
	}
	return out
}
