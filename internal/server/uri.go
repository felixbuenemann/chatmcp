package server

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	URIScheme         = "chatmcp"
	URITopics         = "chatmcp://topics"
	URITopicPrefix    = "chatmcp://topic/"
	URIInboxPrefix    = "chatmcp://inbox/"
	URITopicTemplate  = "chatmcp://topic/{name}{?since,limit}"
	URIInboxTemplate  = "chatmcp://inbox/{name}"
)

type URIKind int

const (
	URIUnknown URIKind = iota
	URIKindTopics
	URIKindTopic
	URIKindInbox
)

type ParsedURI struct {
	Kind   URIKind
	Name   string // topic or agent name
	Since  int64  // 0 if absent
	Limit  int    // 0 if absent
	Raw    string
	Canonical string // URI without query; what subscribers should match
}

func ParseURI(raw string) (*ParsedURI, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != URIScheme {
		return nil, fmt.Errorf("uri scheme must be %q, got %q", URIScheme, u.Scheme)
	}

	host := u.Host
	path := strings.TrimPrefix(u.Path, "/")

	out := &ParsedURI{Raw: raw}

	switch host {
	case "topics":
		if path != "" {
			return nil, errors.New("chatmcp://topics takes no path")
		}
		out.Kind = URIKindTopics
		out.Canonical = URITopics
	case "topic":
		if path == "" {
			return nil, errors.New("chatmcp://topic/ requires a topic name")
		}
		out.Kind = URIKindTopic
		out.Name = path
		out.Canonical = URITopicPrefix + path
	case "inbox":
		if path == "" {
			return nil, errors.New("chatmcp://inbox/ requires an agent name")
		}
		out.Kind = URIKindInbox
		out.Name = path
		out.Canonical = URIInboxPrefix + path
	default:
		return nil, fmt.Errorf("unknown chatmcp uri authority %q", host)
	}

	q := u.Query()
	if v := q.Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid since: %w", err)
		}
		out.Since = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid limit: %w", err)
		}
		out.Limit = n
	}

	return out, nil
}

func TopicURI(name string) string  { return URITopicPrefix + name }
func InboxURI(agent string) string { return URIInboxPrefix + agent }
