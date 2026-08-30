package facebook

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/timelinize/timelinize/datasources/media"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// places_you_have_been_tagged_in.json (year 2024+)
type fbTaggedPlaces []struct {
	Media       []any          `json:"media"`
	LabelValues []fbLabelValue `json:"label_values"`
	FBID        string         `json:"fbid"` // Facebook ID
}

// Generated January 2023
type profileInfo struct {
	ProfileV2 struct {
		Name struct {
			FullName   string `json:"full_name"`
			FirstName  string `json:"first_name"`
			MiddleName string `json:"middle_name"`
			LastName   string `json:"last_name"`
		} `json:"name"`
		Emails struct {
			Emails          []string `json:"emails"`
			PreviousEmails  []string `json:"previous_emails"`
			PendingEmails   []any    `json:"pending_emails"`
			AdAccountEmails []any    `json:"ad_account_emails"`
		} `json:"emails"`
		Birthday fbDate `json:"birthday"`
		Gender   struct {
			GenderOption string `json:"gender_option"`
			Pronoun      string `json:"pronoun"`
		} `json:"gender"`
		PreviousNames []any `json:"previous_names"`
		OtherNames    []struct {
			Name      string `json:"name"`
			Type      string `json:"type"`
			Timestamp int    `json:"timestamp"`
		} `json:"other_names"`
		Hometown struct {
			Name      string `json:"name"`
			Timestamp int    `json:"timestamp"`
		} `json:"hometown"`
		Relationship struct {
			Status      string `json:"status"`
			Partner     string `json:"partner"`
			Anniversary fbDate `json:"anniversary"`
			Timestamp   int    `json:"timestamp"`
		} `json:"relationship"`
		FamilyMembers []struct {
			Name      string `json:"name"`
			Relation  string `json:"relation"`
			Timestamp int    `json:"timestamp"`
		} `json:"family_members"`
		EducationExperiences []struct {
			Name           string   `json:"name"`
			StartTimestamp int      `json:"start_timestamp,omitempty"`
			EndTimestamp   int      `json:"end_timestamp"`
			Graduated      bool     `json:"graduated"`
			Concentrations []string `json:"concentrations"`
			Degree         string   `json:"degree,omitempty"`
			SchoolType     string   `json:"school_type"`
			Timestamp      int      `json:"timestamp"`
		} `json:"education_experiences"`
		WorkExperiences []any `json:"work_experiences"`
		BloodInfo       struct {
			BloodDonorStatus string `json:"blood_donor_status"`
		} `json:"blood_info"`
		Websites []struct {
			Address string `json:"address"`
		} `json:"websites"`
		PhoneNumbers []struct {
			PhoneType   string `json:"phone_type"`
			PhoneNumber string `json:"phone_number"`
			Verified    bool   `json:"verified"`
		} `json:"phone_numbers"`
		Username              string `json:"username"`
		RegistrationTimestamp int    `json:"registration_timestamp"`
		ProfileURI            string `json:"profile_uri"`
	} `json:"profile_v2"`
}

type fbDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

func (d fbDate) isZero() bool { return d.Year == 0 && d.Month == 0 && d.Day == 0 }

// time returns midnight UTC of the date (Facebook gives no time of day for these).
func (d fbDate) time() time.Time {
	return time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, time.UTC)
}

func (d fbDate) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day) }

// fbPlace is a place attached to a post or a life event.
type fbPlace struct {
	Name       string `json:"name"`
	Coordinate struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"coordinate"`
	Address string `json:"address"`
	URL     string `json:"url"`
}

func (p fbPlace) isEmpty() bool {
	return p.Name == "" && p.Address == "" && p.URL == "" &&
		(p.Coordinate.Latitude == 0 || p.Coordinate.Longitude == 0)
}

// entity converts the place to a place entity (coordinates on the address attribute).
func (p fbPlace) entity() *timeline.Entity {
	address := timeline.Attribute{
		Name:  "address",
		Value: FixString(p.Address),
	}
	if p.Coordinate.Latitude != 0 && p.Coordinate.Longitude != 0 {
		lat, lon := p.Coordinate.Latitude, p.Coordinate.Longitude
		address.Latitude = &lat
		address.Longitude = &lon
	}
	return &timeline.Entity{
		Type:       timeline.EntityPlace,
		Name:       FixString(p.Name),
		Attributes: []timeline.Attribute{address},
	}
}

type yourPosts []struct {
	Timestamp   int64 `json:"timestamp"`
	Attachments []struct {
		Data []struct {
			Text            string `json:"text"`
			ExternalContext struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"external_context"`
			Media fbArchiveMedia `json:"media"`
			Place fbPlace        `json:"place"`
			// "Nikolay added a life event from 13 April 2023: PhD, University of Cambridge"
			LifeEvent struct {
				Title       string           `json:"title"`
				Description string           `json:"description"`
				StartDate   fbDate           `json:"start_date"`
				EndDate     fbDate           `json:"end_date"`
				Place       fbPlace          `json:"place"`
				Photos      []fbArchiveMedia `json:"photos"`
			} `json:"life_event"`
			// "Nikolay shared an event." / "Nikolay was attending X at Y."
			Event struct {
				Name           string `json:"name"`
				StartTimestamp int64  `json:"start_timestamp"`
				EndTimestamp   int64  `json:"end_timestamp"`
			} `json:"event"`
		} `json:"data"`
	} `json:"attachments,omitempty"`
	Data []struct {
		Post string `json:"post"`
		// life events carry the date the event is placed at (the post timestamp is when it was added)
		BackdatedTimestamp int64 `json:"backdated_timestamp"`
		UpdateTimestamp    int64 `json:"update_timestamp"`
	} `json:"data"`
	Title string `json:"title,omitempty"`
	Tags  []struct {
		Name string `json:"name"`
	} `json:"tags,omitempty"`
}

type fbYourUncategorizedPhotos struct {
	OtherPhotosV2 []fbArchiveMedia `json:"other_photos_v2"`
}

type fbYourVideos struct {
	VideosV2 []fbArchiveMedia `json:"videos_v2"`
}

type fbArchiveMedia struct {
	URI               string `json:"uri"`
	CreationTimestamp int64  `json:"creation_timestamp"`
	MediaMetadata     struct {
		PhotoMetadata *struct {
			EXIFData []fbEXIFData `json:"exif_data"`
		} `json:"photo_metadata"`
		VideoMetadata *struct {
			EXIFData []struct {
				UploadIP        string `json:"upload_ip"`
				UploadTimestamp int64  `json:"upload_timestamp"`
			} `json:"exif_data"`
		} `json:"video_metadata"`
	} `json:"media_metadata"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

func (m fbArchiveMedia) fillItem(item *timeline.Item, d timeline.DirEntry, postText string, logger *zap.Logger) {
	// the media item might not be in this archive if it's a multi-archive export, so set a retrieval key so we can
	// fill the item in later
	retKey := retrievalKey(d, m.URI)
	item.Retrieval.SetKey(retKey)
	item.Retrieval.FieldUpdatePolicies = map[string]timeline.FieldUpdatePolicy{
		"intermediate_location": timeline.UpdatePolicyPreferExisting,
		"timestamp":             timeline.UpdatePolicyPreferIncoming,
		"location":              timeline.UpdatePolicyPreferIncoming,
		"owner":                 timeline.UpdatePolicyPreferIncoming,
		"data":                  timeline.UpdatePolicyPreferExisting,
		"metadata":              timeline.UpdatePolicyPreferIncoming,
	}

	item.IntermediateLocation = m.URI

	if m.CreationTimestamp > 0 {
		item.Timestamp = time.Unix(m.CreationTimestamp, 0).UTC()
	}

	if item.Content.Data == nil {
		item.Content = timeline.ItemData{
			Filename: path.Base(m.URI),
		}
		// The media file may live in a different archive of a multi-part export, in which case a
		// separate media walk fills the data in later via the retrieval key. But when the file is
		// right here (always the case for message attachments, which no media walk covers), read
		// it directly so the item does not end up without data.
		if info, err := fs.Stat(d.FS, m.URI); err == nil && !info.IsDir() {
			uri := m.URI
			item.Content.Size = uint64(info.Size()) //nolint:gosec // file sizes are non-negative
			item.Content.Data = func(_ context.Context) (io.ReadCloser, error) {
				return d.FS.Open(uri)
			}
			if _, err := media.ExtractAllMetadata(logger, d.FS, uri, item, timeline.MetaMergeAppend); err != nil {
				logger.Debug("extracting metadata from archive media", zap.String("file", uri), zap.Error(err))
			}
		} else if logger != nil {
			logger.Warn("media file referenced by archive not found in this archive; expecting it in another part",
				zap.String("uri", m.URI))
		}
	}

	if item.Metadata == nil {
		item.Metadata = make(timeline.Metadata)
	}

	item.Metadata["Title"] = FixString(m.Title)
	if desc := FixString(m.Description); desc != postText {
		// TODO: this filter probably doesn't work with multiple attachment data where some are text with the same description
		item.Metadata["Description"] = FixString(m.Description)
	}

	// TODO: use media importer functions for this as well...
	// Collect metadata... while we do, prefer TakenTimestamp over CreationTimestamp;
	// I think TakenTimestamp is when the media was captured, and CreationTimestamp is
	// when the post or attachment was created on Facebook. I think.
	// We also include all the EXIF data Facebook gives us because they strip the EXIF
	// data from the files they give us.
	if m.MediaMetadata.PhotoMetadata != nil {
		for _, exif := range m.MediaMetadata.PhotoMetadata.EXIFData {
			if exif.TakenTimestamp != 0 { // negative is valid! (pre-1970)
				item.Timestamp = time.Unix(exif.TakenTimestamp, 0).UTC()

				// I've seen this happen where the value is -62169958800 (for multiple items!)
				// which results in a Very Wrong Timestamp. I don't know what to make of this
				// value, so we have to simply throw it away.
				if item.Timestamp.Year() <= 0 {
					logger.Warn("data source provided TakenTimestamp that is before year 0; not using it", zap.Time("taken_timestamp", item.Timestamp))
					item.Timestamp = time.Time{}
				}
			}
			if lat := exif.Latitude; lat != 0 {
				item.Location.Latitude = &lat
			}
			if lon := exif.Longitude; lon != 0 {
				item.Location.Longitude = &lon
			}
			item.Metadata["ISO"] = exif.ISO
			item.Metadata["Focal length"] = exif.FocalLength
			item.Metadata["Upload IP"] = exif.UploadIP
			if exif.ModifiedTimestamp > 0 {
				item.Metadata["Modified"] = time.Unix(exif.ModifiedTimestamp, 0).UTC()
			}
			item.Metadata["Camera make"] = exif.CameraMake
			item.Metadata["Camera model"] = exif.CameraModel
			item.Metadata["Exposure"] = exif.Exposure
			item.Metadata["F-stop"] = exif.FStop
			item.Metadata["Orientation"] = exif.Orientation
			item.Metadata["Original width"] = exif.OriginalWidth
			item.Metadata["Original height"] = exif.OriginalHeight
		}
	} else if m.MediaMetadata.VideoMetadata != nil {
		for _, exif := range m.MediaMetadata.VideoMetadata.EXIFData {
			item.Metadata["Upload IP"] = exif.UploadTimestamp
			item.Metadata["Upload timestamp"] = time.Unix(exif.UploadTimestamp, 0).UTC()
		}
	}
}

func retrievalKey(d timeline.DirEntry, pathOrURI string) string {
	archiveName := filepath.Base(d.FullPath())
	splitAt := strings.LastIndex(archiveName, "-")
	exportID := archiveName
	if splitAt > -1 {
		exportID = archiveName[:splitAt]
	}
	return fmt.Sprintf("facebook::%s::%s", exportID, pathOrURI)
}

type fbMessengerThread struct {
	Participants []struct {
		Name string `json:"name"`
	} `json:"participants"`
	Messages []struct {
		SenderName  string  `json:"sender_name"`
		TimestampMS int64   `json:"timestamp_ms"`
		IsUnsent    bool    `json:"is_unsent,omitempty"`
		Content     string  `json:"content,omitempty"`
		Share       fbShare `json:"share,omitempty"`
		Reactions   []struct {
			Reaction string `json:"reaction"`
			Actor    string `json:"actor"`
		} `json:"reactions,omitempty"`
		Photos     []fbArchiveMedia `json:"photos,omitempty"`
		Videos     []fbArchiveMedia `json:"videos,omitempty"`
		GIFs       []fbArchiveMedia `json:"gifs,omitempty"`
		AudioFiles []fbArchiveMedia `json:"audio_files,omitempty"`
		Sticker    fbArchiveMedia   `json:"sticker,omitempty"`
	} `json:"messages"`
	Title              string `json:"title"`
	IsStillParticipant bool   `json:"is_still_participant"`
	IsPending          bool   `json:"is_pending"` // message requests
	ThreadPath         string `json:"thread_path"`
	MagicWords         []any  `json:"magic_words"`
}

// sentTo returns the recipients of a message: every participant except the sender.
// Anonymous participants ("Facebook user", "") resolve to the thread's anonymous entity.
func (thread fbMessengerThread) sentTo(sender timeline.Entity, dsName, threadPath string) []*timeline.Entity {
	var sentTo []*timeline.Entity
	seen := make(map[string]bool)
	for _, participant := range thread.Participants {
		e := participantEntity(dsName, FixString(participant.Name), threadPath)
		if sameParticipant(e, sender) {
			continue
		}
		key := e.Attributes[0].Name + "=" + fmt.Sprint(e.Attributes[0].Value)
		if seen[key] {
			continue // several anonymous participants collapse into one entity
		}
		seen[key] = true
		sentTo = append(sentTo, &e)
	}
	return sentTo
}

// metadata returns thread-level facts worth keeping on every message of the thread:
//   - "Thread title" when the title is not simply the counterpart's name (marketplace
//     conversations, group chats)
//   - "Message request": true for threads in message_requests (is_pending)
//   - "Thread participants": "partial" when a sender is not in the participant list
//     (the owner left the group and Facebook truncated the list)
func (thread fbMessengerThread) metadata(dsName, threadPath, ownerName string) timeline.Metadata {
	meta := make(timeline.Metadata)
	title := FixString(thread.Title)
	participants := make(map[string]bool)
	var counterpart string
	for _, p := range thread.Participants {
		name := FixString(p.Name)
		participants[name] = true
		if name != ownerName && counterpart == "" {
			counterpart = name
		}
	}
	if title != "" && title != counterpart && !(counterpart == "" && title == ownerName) {
		meta["Thread title"] = title
	}
	if thread.IsPending {
		meta["Message request"] = true
	}
	for _, m := range thread.Messages {
		if name := FixString(m.SenderName); !participants[name] && !isAnonymousName(name) {
			meta["Thread participants"] = "partial"
			break
		}
	}
	return meta
}

type fbAlbumMeta struct {
	Name   string `json:"name"`
	Photos []struct {
		URI               string `json:"uri"`
		CreationTimestamp int64  `json:"creation_timestamp"`
		MediaMetadata     struct {
			PhotoMetadata struct {
				EXIFData []fbEXIFData `json:"exif_data"`
			} `json:"photo_metadata"`
		} `json:"media_metadata"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"photos"`
	CoverPhoto struct {
		URI               string `json:"uri"`
		CreationTimestamp int64  `json:"creation_timestamp"`
		MediaMetadata     struct {
			PhotoMetadata struct {
				EXIFData []fbEXIFData `json:"exif_data"`
			} `json:"photo_metadata"`
		} `json:"media_metadata"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"cover_photo"`
	LastModifiedTimestamp int64  `json:"last_modified_timestamp"`
	Description           string `json:"description"`
}

type fbEXIFData struct {
	ISO               int     `json:"iso"`
	FocalLength       string  `json:"focal_length"`
	UploadIP          string  `json:"upload_ip"`
	TakenTimestamp    int64   `json:"taken_timestamp"`
	ModifiedTimestamp int64   `json:"modified_timestamp"`
	CameraMake        string  `json:"camera_make"`
	CameraModel       string  `json:"camera_model"`
	Exposure          string  `json:"exposure"`
	FStop             string  `json:"f_stop"`
	Orientation       int     `json:"orientation"`
	OriginalWidth     int     `json:"original_width"`
	OriginalHeight    int     `json:"original_height"`
	Latitude          float64 `json:"latitude"`
	Longitude         float64 `json:"longitude"`
}

type fbLabelValue struct {
	Title          string `json:"title,omitempty"`
	Label          string `json:"label"`
	Value          string `json:"value,omitempty"`
	TimestampValue int    `json:"timestamp_value,omitempty"`
	Dict           []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"dict,omitempty"`
}

type fbCheckIns []fbCheckIn

type fbCheckIn struct {
	Timestamp   int            `json:"timestamp"`
	Media       []any          `json:"media"`
	LabelValues []fbLabelValue `json:"label_values"`
	FBID        string         `json:"fbid"`
}

// parseCoordsFromDictString parses coordinates from the dict label value.
// Expected format: "(38.575804323267 , -121.4804199327)" which is (lat, lon) order
//
//nolint:mnd
func parseCoordsFromDictString(coordStr string) (lat, lon float64, err error) {
	// remove fluff
	coordStr = strings.TrimSpace(coordStr)
	coordStr = strings.TrimPrefix(coordStr, "(")
	coordStr = strings.TrimSuffix(coordStr, ")")

	// split lat/lon and parse each float
	parts := strings.SplitN(coordStr, ",", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid lat/lon string: %q", coordStr)
	}
	lat, err = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid latitude float: %w", err)
	}
	lon, err = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid longitude float: %w", err)
	}

	if lat < -90 || lat > 90 {
		return 0, 0, fmt.Errorf("invalid latitude: %f", lat)
	}
	if lon < -180 || lon > 180 {
		return 0, 0, fmt.Errorf("invalid longitude: %f", lon)
	}

	return
}
