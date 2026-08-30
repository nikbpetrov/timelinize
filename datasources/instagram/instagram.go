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

// Package instagram implements a data source for importing data from Instagram archive files.
package instagram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"time"

	"github.com/timelinize/timelinize/datasources/facebook"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "instagram",
		Title:           "Instagram",
		Icon:            "instagram.svg",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(Client) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Options configures an Instagram import.
type Options struct {
	facebook.Filters
}

// Client implements the timeline.Client interface.
type Client struct{}

// Recognize returns whether the file or folder is recognized.
func (Client) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	if dirEntry.FileExists("personal_information/personal_information/instagram_profile_information.json") &&
		dirEntry.FileExists("your_instagram_activity") {
		return timeline.Recognition{Confidence: 1.0}, nil
	}
	if dirEntry.FileExists("media") &&
		(dirEntry.FileExists(personalInformationPath2025) ||
			dirEntry.FileExists(personalInformationPathPre2025) ||
			dirEntry.FileExists(personalInformation2021)) {
		return timeline.Recognition{Confidence: .95}, nil
	}
	return timeline.Recognition{}, nil
}

// FileImport imports data from the file or folder.
func (c *Client) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	dsOpt, _ := params.DataSourceOptions.(*Options)
	if dsOpt == nil {
		dsOpt = new(Options)
	}

	// first, load the profile information
	pi, err := c.getPersonalInfo(dirEntry.FS)
	if err != nil {
		return fmt.Errorf("loading profile: %w", err)
	}
	if len(pi.ProfileUser) == 0 {
		return errors.New("no profile information found: missing profile user")
	}
	personalInfo := pi.ProfileUser[0].StringMapData

	owner := timeline.Entity{
		Name: personalInfo.Name.Value,
		Attributes: []timeline.Attribute{
			{
				Name:     "instagram_username",
				Value:    personalInfo.Username.Value,
				Identity: true,
			},
			{
				Name:  timeline.AttributeGender,
				Value: personalInfo.Gender.Value,
			},
			{
				Name:        timeline.AttributePhoneNumber,
				Value:       personalInfo.PhoneNumber.Value,
				Identifying: true,
			},
			{
				Name:        timeline.AttributeEmail,
				Value:       personalInfo.Email.Value,
				Identifying: true,
			},
			{
				Name:  "instagram_bio",
				Value: personalInfo.Bio.Value,
			},
			{
				Name:  "website",
				Value: personalInfo.Website.Value,
			},
		},
	}
	if picFilename := pi.ProfileUser[0].MediaMapData.ProfilePhoto.URI; picFilename != "" {
		owner.NewPicture = func(_ context.Context) (io.ReadCloser, error) {
			return dirEntry.FS.Open(picFilename)
		}
	}
	if personalInfo.DateOfBirth.Value != "" && personalInfo.DateOfBirth.Value != "1919-01-01" { // for some weird reason their default is 1919??
		bd, err := time.Parse("2006-01-02", personalInfo.DateOfBirth.Value)
		if err == nil {
			owner.Attributes = append(owner.Attributes, timeline.Attribute{
				Name:  "birth_date",
				Value: bd,
			})
		}
	}

	// then, load the posts index
	postIdx, err := c.getPostsIndex(dirEntry.FS)
	if err != nil {
		return fmt.Errorf("loading index: %w", err)
	}
	for i, post := range postIdx {
		if dsOpt.PostsLimited(i) {
			break
		}

		// a post may have multiple media items, we'll treat them as attachments

		var ig *timeline.Graph
		var firstMedia int

		// if there is text, use that as the "main" item
		if postText := post.allText(); postText != "" {
			ig = &timeline.Graph{
				Item: &timeline.Item{
					Classification: timeline.ClassSocial,
					Timestamp:      post.timestamp(),
					Owner:          owner,
					Content: timeline.ItemData{
						Data: timeline.StringData(postText),
					},
					IntermediateLocation: post.filename,
				},
			}
		} else if len(post.Media) > 0 {
			item := post.Media[0].timelineItem(dirEntry.FS, owner)
			ig = &timeline.Graph{Item: item}
			firstMedia = 1 // the 0th media was used as the root of the graph
		}

		// add remaining media to graph
		for i := firstMedia; i < len(post.Media); i++ {
			ig.ToItem(timeline.RelAttachment, post.Media[i].timelineItem(dirEntry.FS, owner))
		}

		params.Pipeline <- ig
	}

	// stories
	// TODO: Maybe stories should go into a collection
	storyIdx, err := c.getStoryIndex(dirEntry.FS, params.Log)
	if err != nil {
		return err
	}
	for i, story := range storyIdx.IgStories {
		if dsOpt.StoriesLimited(i) {
			break
		}
		params.Pipeline <- &timeline.Graph{Item: storyItem(dirEntry, owner, story)}
	}

	// messages
	err = facebook.GetMessages(ctx, "instagram", dirEntry, params, dsOpt.Filters, facebook.MessageContext{
		OwnerUsername: personalInfo.Username.Value,
		OwnStory:      storyIdx.lookup(dirEntry, owner),
	})
	if err != nil {
		return err
	}

	return nil
}

func (c *Client) getPersonalInfo(fsys fs.FS) (instaPersonalInformation, error) {
	var pi instaPersonalInformation

	file, err := fsys.Open(personalInformationPathPre2025)
	if errors.Is(err, fs.ErrNotExist) {
		file, err = fsys.Open(personalInformationPath2025)
	}
	if errors.Is(err, fs.ErrNotExist) {
		file, err = fsys.Open(personalInformation2021)
	}
	if err != nil {
		return pi, err
	}
	defer file.Close()

	err = json.NewDecoder(file).Decode(&pi)
	if err != nil {
		return pi, fmt.Errorf("decoding personal information file: %w", err)
	}
	return pi, nil
}

func (c *Client) getPostsIndex(fsys fs.FS) (instaPostsIndex, error) {
	var all instaPostsIndex

	makePostsFilename := func(prefix string, i int) string {
		return fmt.Sprintf("%s%d.json", prefix, i)
	}

	for i := 1; i < 10000; i++ {
		// try different paths until we get the one that exists (the archive layout changed over the years)
		postsFilename := makePostsFilename(instaPostsIndexPrefix2025, i)
		file, err := fsys.Open(postsFilename)
		if errors.Is(err, fs.ErrNotExist) {
			postsFilename = makePostsFilename(instaPostsIndexPrefixPre2025, i)
			file, err = fsys.Open(postsFilename)
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
		}
		if err != nil {
			return nil, err
		}

		var idx instaPostsIndex
		err = json.NewDecoder(file).Decode(&idx)
		file.Close()
		if err != nil {
			return nil, fmt.Errorf("decoding posts index file %s: %w", postsFilename, err)
		}

		for i := range idx {
			idx[i].filename = postsFilename
		}

		all = append(all, idx...)
	}

	return all, nil
}

func (c *Client) getStoryIndex(fsys fs.FS, logger *zap.Logger) (instaStories, error) {
	file, err := fsys.Open(instaStoryIndex2025)
	if errors.Is(err, fs.ErrNotExist) {
		file, err = fsys.Open(instaStoryIndexPre2025)
	}
	if errors.Is(err, fs.ErrNotExist) {
		logger.Warn("no Instagram stories found")
		return instaStories{}, nil
	}
	if err != nil {
		return instaStories{}, err
	}
	defer file.Close()

	var idx instaStories
	err = json.NewDecoder(file).Decode(&idx)
	if err != nil {
		return idx, fmt.Errorf("decoding stories index file: %w", err)
	}

	return idx, nil
}

// storyItem builds the item for one of the owner's stories.
func storyItem(dirEntry timeline.DirEntry, owner timeline.Entity, story instaStory) *timeline.Item {
	it := &timeline.Item{
		Timestamp:            time.Unix(story.CreationTimestamp, 0).UTC(),
		Owner:                owner,
		IntermediateLocation: story.URI,
		Content: timeline.ItemData{
			Filename: path.Base(story.URI),
			Data: func(_ context.Context) (io.ReadCloser, error) {
				return dirEntry.FS.Open(story.URI)
			},
		},
		Metadata: timeline.Metadata{
			"Caption": facebook.FixString(story.Title),
		},
	}
	// The same story may be emitted twice in one import (by the stories loop and by a DM that
	// links to it); batches are processed concurrently, so key it for the pipeline to fuse them.
	it.Retrieval.SetKey("instagram::story::" + story.URI)
	return it
}

// ownStoryMatchTolerance is how far the time decoded from a story's media ID may be
// from the story's creation_timestamp in the export (observed: 1-90 s, ID time first).
const ownStoryMatchTolerance = 5 * time.Minute

// lookup returns a function that finds the owner's story published nearest to a time,
// building an item identical to the one emitted for the story so the pipeline matches it.
func (idx instaStories) lookup(dirEntry timeline.DirEntry, owner timeline.Entity) func(time.Time) *timeline.Item {
	stories := make([]instaStory, len(idx.IgStories))
	copy(stories, idx.IgStories)
	sort.Slice(stories, func(i, j int) bool { return stories[i].CreationTimestamp < stories[j].CreationTimestamp })
	return func(t time.Time) *timeline.Item {
		if len(stories) == 0 {
			return nil
		}
		sec := t.Unix()
		i := sort.Search(len(stories), func(i int) bool { return stories[i].CreationTimestamp >= sec })
		best := -1
		for _, j := range []int{i - 1, i} {
			if j < 0 || j >= len(stories) {
				continue
			}
			if best == -1 || abs(stories[j].CreationTimestamp-sec) < abs(stories[best].CreationTimestamp-sec) {
				best = j
			}
		}
		if best == -1 || abs(stories[best].CreationTimestamp-sec) > int64(ownStoryMatchTolerance.Seconds()) {
			return nil
		}
		return storyItem(dirEntry, owner, stories[best])
	}
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

const (
	personalInformation2021        = "account_information/personal_information.json"
	personalInformationPathPre2025 = "personal_information/personal_information.json"
	personalInformationPath2025    = "personal_information/personal_information/personal_information.json"

	instaPostsIndexPrefixPre2025 = "content/posts_"
	instaPostsIndexPrefix2025    = "your_instagram_activity/media/posts_"

	instaStoryIndexPre2025 = "content/stories.json"
	instaStoryIndex2025    = "your_instagram_activity/media/stories.json"
)
