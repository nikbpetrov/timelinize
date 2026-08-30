package facebook

import (
	"path"
	"regexp"
	"strings"

	"github.com/timelinize/timelinize/timeline"
)

// Meta exports anonymise people who deleted their account (or whose name Facebook will
// not reveal): the sender/participant name becomes "Facebook user" (or "" in older
// threads whose folder is just the numeric thread id). Identifying entities by name would
// merge every such person into one; instead each anonymous person is identified by the
// thread they appear in — one entity per thread, never merged across threads.

var threadIDRegex = regexp.MustCompile(`(\d+)$`)

// threadID returns the stable numeric id of a thread from its folder name
// ("inbox/somebody_123" -> "123", "inbox/123" -> "123"). Falls back to the folder name.
func threadID(threadPath string) string {
	base := path.Base(threadPath)
	id := base
	if m := threadIDRegex.FindStringSubmatch(base); m != nil {
		id = m[1]
	}
	if strings.HasPrefix(threadPath, "e2ee/") {
		// the E2EE export numbers its threads from 1; keep them apart from the main export's ids
		return "e2ee-" + id
	}
	return id
}

// isAnonymousName reports whether a participant/sender name is one of Meta's placeholders
// for a person it cannot or will not name.
func isAnonymousName(name string) bool {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "", "facebook user", "instagram user":
		return true
	}
	return false
}

// participantEntity builds the entity for a sender, participant or reaction actor of a
// thread. Named people are identified by their display name on the platform (as before);
// anonymous ones by the thread id (see above).
func participantEntity(dsName, name, threadPath string) timeline.Entity {
	if isAnonymousName(name) {
		display := "Facebook user"
		if dsName == "instagram" {
			display = "Instagram user"
		}
		return timeline.Entity{
			Name: display,
			Attributes: []timeline.Attribute{
				{
					Name:     dsName + "_thread_user",
					Value:    threadID(threadPath),
					Identity: true,
				},
			},
		}
	}
	return timeline.Entity{
		Name: name,
		Attributes: []timeline.Attribute{
			{
				Name:     dsName + "_name",
				Value:    name,
				Identity: true,
			},
		},
	}
}

// sameParticipant reports whether two participant entities denote the same person
// (same identity attribute).
func sameParticipant(a, b timeline.Entity) bool {
	if len(a.Attributes) == 0 || len(b.Attributes) == 0 {
		return a.Name == b.Name
	}
	return a.Attributes[0].Name == b.Attributes[0].Name && a.Attributes[0].Value == b.Attributes[0].Value
}
