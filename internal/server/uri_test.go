package server

import "testing"

func TestParseURI(t *testing.T) {
	cases := []struct {
		raw        string
		wantKind   URIKind
		wantName   string
		wantSince  int64
		wantLimit  int
		wantCanon  string
		wantErr    bool
	}{
		{"chatmcp://topics", URIKindTopics, "", 0, 0, "chatmcp://topics", false},
		{"chatmcp://topic/general", URIKindTopic, "general", 0, 0, "chatmcp://topic/general", false},
		{"chatmcp://topic/general?since=42&limit=10", URIKindTopic, "general", 42, 10, "chatmcp://topic/general", false},
		{"chatmcp://inbox/alice", URIKindInbox, "alice", 0, 0, "chatmcp://inbox/alice", false},
		{"chatmcp://unknown", 0, "", 0, 0, "", true},
		{"https://other.example/", 0, "", 0, 0, "", true},
		{"chatmcp://topic/", 0, "", 0, 0, "", true},
	}
	for _, c := range cases {
		got, err := ParseURI(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseURI(%q): want error, got %+v", c.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseURI(%q): unexpected err %v", c.raw, err)
			continue
		}
		if got.Kind != c.wantKind || got.Name != c.wantName ||
			got.Since != c.wantSince || got.Limit != c.wantLimit ||
			got.Canonical != c.wantCanon {
			t.Errorf("ParseURI(%q): got %+v, want kind=%v name=%q since=%d limit=%d canon=%q",
				c.raw, got, c.wantKind, c.wantName, c.wantSince, c.wantLimit, c.wantCanon)
		}
	}
}
