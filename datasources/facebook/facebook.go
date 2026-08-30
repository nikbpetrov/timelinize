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

// Package facebook implements the Facebook service by supporting account
// export files and also the Graph API: https://developers.facebook.com/docs/graph-api
package facebook

import (
	"strings"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "facebook",
		Title:           "Facebook",
		Icon:            "facebook.svg",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(Archive) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

type Options struct {
	// The Facebook username of the account from whence this data came.
	// Required input, since with multi-archive exports, there's no
	// guarantee that the profile information is in the first archive.
	Username string `json:"username,omitempty"`

	Filters
}

// Filters restricts what is imported from a Meta (Facebook/Instagram)
// archive. Zero values mean "no limit". Intended for development and
// testing against large exports, where a full import takes a long time.
type Filters struct {
	// Import at most this many posts (0 = all).
	MaxPosts int `json:"max_posts,omitempty"`

	// Import at most this many stories (0 = all).
	MaxStories int `json:"max_stories,omitempty"`

	// Only import message threads whose folder name (e.g.
	// "inbox/somebody_1234567890") contains one of these strings
	// (empty = all threads).
	Conversations []string `json:"conversations,omitempty"`

	// Import at most this many messages per thread, newest first
	// (0 = all). Attachments of an imported message are always included.
	MaxMessagesPerConversation int `json:"max_messages_per_conversation,omitempty"`
}

func (f Filters) postsLimited(count int) bool   { return f.MaxPosts > 0 && count >= f.MaxPosts }
func (f Filters) storiesLimited(count int) bool { return f.MaxStories > 0 && count >= f.MaxStories }

// PostsLimited reports whether count posts already exceed the configured limit.
func (f Filters) PostsLimited(count int) bool { return f.postsLimited(count) }

// StoriesLimited reports whether count stories already exceed the configured limit.
func (f Filters) StoriesLimited(count int) bool { return f.storiesLimited(count) }

// wantThread reports whether the message thread at threadPath
// (relative to the messages folder, e.g. "inbox/somebody_123") passes the filter.
func (f Filters) wantThread(threadPath string) bool {
	if len(f.Conversations) == 0 {
		return true
	}
	for _, c := range f.Conversations {
		if c != "" && strings.Contains(threadPath, c) {
			return true
		}
	}
	return false
}
