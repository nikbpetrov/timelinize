package facebook

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Meta exports carry many things as a "message" that nobody typed: call records, group
// membership changes, nickname/theme changes, placeholders for reactions and attachments,
// location shares. Nothing in the JSON distinguishes them except the (English) sentence
// in content and a few optional fields, so the rules are hard-coded here, in one place,
// and covered by classify_test.go. See docs/fork/messenger-plan.md §2.

type messageKind string

const (
	kindMessage        messageKind = "message"         // a person talking (text, media, share, link)
	kindPlaceholder    messageKind = "placeholder"     // "X sent an attachment." — text dropped, attachments kept
	kindPseudoReaction messageKind = "pseudo_reaction" // "Reacted 😂 to your message" — dropped (the reaction is in reactions[])
	kindCall           messageKind = "call"            // call_duration present
	kindCallEvent      messageKind = "call_event"      // "X joined the video call." — system
	kindGroupEvent     messageKind = "group_event"     // membership/name/photo/admin changes — system
	kindThreadEvent    messageKind = "thread_event"    // nickname/theme/pin/wave/connected — dropped
	kindLocation       messageKind = "location"        // "X sent a location." + maps link
	kindUnsent         messageKind = "unsent"          // is_unsent — dropped
	kindTakenDown      messageKind = "taken_down"      // is_taken_down — dropped
	kindLink           messageKind = "link"            // content is exactly one URL — text kept + bookmark
)

// classified is what the classifier knows about a message beyond its kind.
type classified struct {
	Kind messageKind

	// call
	Direction string // outgoing, incoming, group
	Video     bool
	Missed    bool
	Duration  int64 // seconds

	// system events (group_event, call_event, thread_event)
	Event   string // member_added, member_left, member_removed, renamed, created, photo_changed, admin, call_joined, …
	Subject string // the other person(s) / the new name, when the sentence carries one

	// location
	Latitude, Longitude *float64
	Address             string

	// link
	URL string
}

// dropped reports whether nothing at all should be imported for this message.
func (c classified) dropped() bool {
	switch c.Kind {
	case kindPseudoReaction, kindThreadEvent, kindUnsent, kindTakenDown:
		return true
	}
	return false
}

// system reports whether the message is a system notice (Kind: system in metadata).
func (c classified) system() bool {
	return c.Kind == kindGroupEvent || c.Kind == kindCallEvent
}

var (
	pseudoReactionRegex = regexp.MustCompile(`^(Reacted .+ to your message|Liked a message)\s*$`)
	bareURLRegex        = regexp.MustCompile(`^https?://\S+$`)

	callOutgoingRegex     = regexp.MustCompile(`^You called .+\.$`)
	callIncomingRegex     = regexp.MustCompile(` called you\.$`)
	callMissedByMeRegex   = regexp.MustCompile(`^You missed a (video |voice )?call`)
	callMissedByThemRegex = regexp.MustCompile(` missed your (video |voice )?call\.$`)
	callGroupEndedRegex   = regexp.MustCompile(`^The (video |voice )?call ended\.$`)
	callEventRegex        = regexp.MustCompile(`( (joined|left) the (video |voice )?call\.| started sharing video| started a (video |voice )?call)`)

	groupEventRegexes = []struct {
		re      *regexp.Regexp
		event   string
		subject int // capture group holding the subject, 0 = none
	}{
		{regexp.MustCompile(`^(.+?) added (.+) to the group\.$`), "member_added", 2},
		{regexp.MustCompile(`^(.+?) removed (.+) from the group\.$`), "member_removed", 2},
		{regexp.MustCompile(`^(.+?) left the group\.$`), "member_left", 0},
		{regexp.MustCompile(`^(.+?) named the group (.+)\.$`), "renamed", 2},
		{regexp.MustCompile(`^(.+?) created the group\.$`), "created", 0},
		{regexp.MustCompile(`^(.+?) changed the group photo\.$`), "photo_changed", 0},
		{regexp.MustCompile(`^(.+?) (is now an admin|removed .+ as an admin)\.$`), "admin", 0},
		{regexp.MustCompile(`^(.+?) added participants\.$`), "member_added", 0},
	}

	threadEventRegex = regexp.MustCompile(`( set the nickname for | set (his|her|their|your) own nickname| cleared the nickname| changed the (chat )?theme| set the emoji | changed the quick reaction|(un)?pinned a message\.$|^You are now connected on Messenger|^Say hi to your new Facebook friend| waved at | turned (on|off) (disappearing|end-to-end)|^This chat is read-only\.$| removed .+'s message\.$)`)

	locationRegex = regexp.MustCompile(` sent a (live )?location\.$`)
	latLonRegex   = regexp.MustCompile(`^(-?\d+(?:\.\d+)?),\s*(-?\d+(?:\.\d+)?)$`)
)

// classifyMessage decides what a message is from its decoded text and fields.
// text must already be FixString'ed; msg fields are used as exported.
func classifyMessage(msg fbMessage, text string) classified {
	text = strings.TrimSpace(text)
	switch {
	case msg.IsTakenDown:
		return classified{Kind: kindTakenDown}
	case msg.IsUnsent:
		return classified{Kind: kindUnsent}
	case msg.CallDuration != nil:
		return classifyCall(msg, text)
	case text == "":
		return classified{Kind: kindMessage}
	case isAttachmentPlaceholder(text):
		return classified{Kind: kindPlaceholder}
	case pseudoReactionRegex.MatchString(text):
		return classified{Kind: kindPseudoReaction}
	case locationRegex.MatchString(text):
		return classifyLocation(msg)
	case callEventRegex.MatchString(text):
		return classified{Kind: kindCallEvent, Event: "call_joined"}
	case threadEventRegex.MatchString(text):
		return classified{Kind: kindThreadEvent}
	case bareURLRegex.MatchString(text):
		return classified{Kind: kindLink, URL: text}
	}
	for _, g := range groupEventRegexes {
		if m := g.re.FindStringSubmatch(text); m != nil {
			c := classified{Kind: kindGroupEvent, Event: g.event}
			if g.subject > 0 {
				c.Subject = m[g.subject]
			}
			return c
		}
	}
	return classified{Kind: kindMessage}
}

func classifyCall(msg fbMessage, text string) classified {
	c := classified{Kind: kindCall, Duration: *msg.CallDuration, Missed: msg.Missed}
	c.Video = strings.Contains(text, "video call")
	switch {
	case callGroupEndedRegex.MatchString(text):
		c.Direction = "group"
	case callOutgoingRegex.MatchString(text):
		c.Direction = "outgoing"
	case callIncomingRegex.MatchString(text):
		c.Direction = "incoming"
	case callMissedByMeRegex.MatchString(text):
		c.Direction, c.Missed = "incoming", true
	case callMissedByThemRegex.MatchString(text):
		c.Direction, c.Missed = "outgoing", true
	}
	if c.Duration == 0 && strings.Contains(text, "missed") {
		c.Missed = true
	}
	return c
}

// classifyLocation reads the coordinates out of the Bing Maps link Messenger attaches to
// "X sent a location." (…/maps/default.aspx?…&where1=42.68,23.32&…); when where1 is an
// address, or there is no link (live locations), the address text is kept instead.
func classifyLocation(msg fbMessage) classified {
	c := classified{Kind: kindLocation}
	if u, err := url.Parse(msg.Share.Link); err == nil && msg.Share.Link != "" {
		where := strings.TrimSpace(u.Query().Get("where1"))
		if m := latLonRegex.FindStringSubmatch(where); m != nil {
			lat, err1 := strconv.ParseFloat(m[1], 64)
			lon, err2 := strconv.ParseFloat(m[2], 64)
			if err1 == nil && err2 == nil {
				c.Latitude, c.Longitude = &lat, &lon
			}
		} else if where != "" {
			c.Address = where
		}
	}
	if addr := strings.TrimSpace(FixString(msg.Share.ShareText)); addr != "" {
		c.Address = addr
	}
	return c
}
