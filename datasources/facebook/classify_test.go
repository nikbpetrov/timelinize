package facebook

import "testing"

// Sentences as they appear in real exports (see docs/fork/messenger-plan.md §2).
func TestClassifyMessage(t *testing.T) {
	dur := func(n int64) *int64 { return &n }
	tests := []struct {
		name string
		msg  fbMessage
		text string
		want classified
	}{
		{"text", fbMessage{}, "see you at 8", classified{Kind: kindMessage}},
		{"empty", fbMessage{}, "", classified{Kind: kindMessage}},
		{"placeholder", fbMessage{}, "Angel sent an attachment.", classified{Kind: kindPlaceholder}},
		{"pseudo reaction trailing space", fbMessage{}, "Reacted 👍 to your message ", classified{Kind: kindPseudoReaction}},
		{"pseudo like", fbMessage{}, "Liked a message", classified{Kind: kindPseudoReaction}},
		{"unsent with content", fbMessage{IsUnsent: true}, "This poll is no longer available.", classified{Kind: kindUnsent}},
		{"taken down", fbMessage{IsTakenDown: true, IsUnsent: true}, "", classified{Kind: kindTakenDown}},
		{"call outgoing", fbMessage{CallDuration: dur(18)}, "You called Angel.", classified{Kind: kindCall, Direction: "outgoing", Duration: 18}},
		{"call incoming", fbMessage{CallDuration: dur(384)}, "Б. called you.", classified{Kind: kindCall, Direction: "incoming", Duration: 384}},
		{"call missed flag", fbMessage{CallDuration: dur(0), Missed: true}, "You missed a call from Цветелина.", classified{Kind: kindCall, Direction: "incoming", Missed: true}},
		{"call missed text only", fbMessage{CallDuration: dur(0)}, "Angel missed your call.", classified{Kind: kindCall, Direction: "outgoing", Missed: true}},
		{"call missed video", fbMessage{CallDuration: dur(0)}, "You missed a video call with Elly.", classified{Kind: kindCall, Direction: "incoming", Missed: true, Video: true}},
		{"call group ended", fbMessage{CallDuration: dur(606)}, "The video call ended.", classified{Kind: kindCall, Direction: "group", Video: true, Duration: 606}},
		{"call contact", fbMessage{CallDuration: dur(5)}, "You called a contact.", classified{Kind: kindCall, Direction: "outgoing", Duration: 5}},
		{"call joined", fbMessage{}, "You joined the call.", classified{Kind: kindCallEvent, Event: "call_joined"}},
		{"call joined video", fbMessage{}, "Иванка joined the video call.", classified{Kind: kindCallEvent, Event: "call_joined"}},
		{"group added", fbMessage{}, "A contact added Alhaji M Juli Badawa and 3 others to the group.", classified{Kind: kindGroupEvent, Event: "member_added", Subject: "Alhaji M Juli Badawa and 3 others"}},
		{"group added participants", fbMessage{}, "A contact added participants.", classified{Kind: kindGroupEvent, Event: "member_added"}},
		{"group left", fbMessage{}, "You left the group.", classified{Kind: kindGroupEvent, Event: "member_left"}},
		{"group named", fbMessage{}, "Arjay named the group Arjay · 2 beds 2 baths Room only.", classified{Kind: kindGroupEvent, Event: "renamed", Subject: "Arjay · 2 beds 2 baths Room only"}},
		{"group created", fbMessage{}, "Orin created the group.", classified{Kind: kindGroupEvent, Event: "created"}},
		{"group removed", fbMessage{}, "Ve Ls removed you from the group.", classified{Kind: kindGroupEvent, Event: "member_removed", Subject: "you"}},
		{"group photo", fbMessage{}, "Trell changed the group photo.", classified{Kind: kindGroupEvent, Event: "photo_changed"}},
		{"group admin", fbMessage{}, "Venessa Bratts is now an admin.", classified{Kind: kindGroupEvent, Event: "admin"}},
		{"nickname", fbMessage{}, "Kalin set the nickname for Katerina Koleva to Katerina Koleva.", classified{Kind: kindThreadEvent}},
		{"theme", fbMessage{}, "Mariana changed the theme to Spain 🇪🇸", classified{Kind: kindThreadEvent}},
		{"unpinned", fbMessage{}, "Мелинда unpinned a message.", classified{Kind: kindThreadEvent}},
		{"connected", fbMessage{}, "You are now connected on Messenger", classified{Kind: kindThreadEvent}},
		{"wave", fbMessage{}, "You waved at a contact!", classified{Kind: kindThreadEvent}},
		{"moderation", fbMessage{}, "An admin, moderator or host removed Elly's message.", classified{Kind: kindThreadEvent}},
		{"read only", fbMessage{}, "This chat is read-only.", classified{Kind: kindThreadEvent}},
		{"bare url", fbMessage{}, "https://youtube.com/watch?v=RnlGqXD_cJc&feature=share", classified{Kind: kindLink, URL: "https://youtube.com/watch?v=RnlGqXD_cJc&feature=share"}},
		{"text with url", fbMessage{}, "look https://youtube.com/watch?v=x haha", classified{Kind: kindMessage}},
		{"talking about the group", fbMessage{}, "who added him to the group?", classified{Kind: kindMessage}},
		{"talking about nicknames", fbMessage{}, "I set the nickname for you yesterday", classified{Kind: kindThreadEvent}}, // accepted false positive: sentence shape is Meta's
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyMessage(tt.msg, tt.text)
			if got != tt.want {
				t.Errorf("classifyMessage(%q) = %+v, want %+v", tt.text, got, tt.want)
			}
		})
	}
}

func TestClassifyLocation(t *testing.T) {
	coords := fbMessage{Share: fbShare{Link: "https://www.bing.com/maps/default.aspx?v=2&pc=FACEBK&mid=8100&where1=42.682305080187%2C+23.320682104874&FORM=FBKPL1&mkt=en-GB"}}
	c := classifyMessage(coords, "Алекс sent a location.")
	if c.Kind != kindLocation || c.Latitude == nil || c.Longitude == nil || *c.Latitude != 42.682305080187 || *c.Longitude != 23.320682104874 {
		t.Fatalf("coords: %+v", c)
	}
	addr := fbMessage{Share: fbShare{Link: "https://www.bing.com/maps/default.aspx?v=2&pc=FACEBK&mid=8100&where1=Gallery+No+14%2C+1408+Sofia&FORM=FBKPL1&mkt=en-GB", ShareText: "Gallery No 14, 1408 Sofia"}}
	c = classifyMessage(addr, "Dimitar sent a location.")
	if c.Kind != kindLocation || c.Latitude != nil || c.Address != "Gallery No 14, 1408 Sofia" {
		t.Fatalf("address: %+v", c)
	}
	live := fbMessage{Share: fbShare{ShareText: "Vladimir sent a live location."}}
	c = classifyMessage(live, "Vladimir sent a live location.")
	if c.Kind != kindLocation || c.Address != "Vladimir sent a live location." {
		t.Fatalf("live: %+v", c)
	}
}
