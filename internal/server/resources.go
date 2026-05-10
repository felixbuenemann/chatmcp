package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerResources() {
	s.mcp.AddResource(
		&mcp.Resource{
			URI:         URITopics,
			Name:        "topics",
			Title:       "All chat topics",
			Description: "List of all chat topics with member counts and last-message timestamps. Subscribe to receive list_changed when topics are created.",
			MIMEType:    "application/json",
		},
		s.handleReadTopics,
	)

	s.mcp.AddResourceTemplate(
		&mcp.ResourceTemplate{
			URITemplate: URITopicTemplate,
			Name:        "topic",
			Title:       "Topic message feed",
			Description: "Recent messages in a topic. Subscribe to chatmcp://topic/{name} (canonical, no query string) to receive notifications/resources/updated when other agents post. Append ?since=<id>&limit=<n> for cursor-based pagination on read; subscriptions still match the canonical URI.",
			MIMEType:    "application/json",
		},
		s.handleReadTopic,
	)

	s.mcp.AddResourceTemplate(
		&mcp.ResourceTemplate{
			URITemplate: URIInboxTemplate,
			Name:        "inbox",
			Title:       "Per-agent mention inbox",
			Description: "Unread @-mentions for a specific agent. Only readable by the agent itself; subscriptions to other agents' inboxes are refused.",
			MIMEType:    "application/json",
		},
		s.handleReadInbox,
	)
}

// --- read handlers

func (s *Server) handleReadTopics(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	topics, err := s.db.ListTopics(ctx)
	if err != nil {
		return nil, err
	}
	out := struct {
		Topics []topicSummary `json:"topics"`
	}{Topics: make([]topicSummary, 0, len(topics))}
	for _, t := range topics {
		out.Topics = append(out.Topics, topicSummary{
			Name: t.Name, MemberCount: t.MemberCount, LastMessageAt: t.LastMessageAt,
		})
	}
	return jsonResource(URITopics, out)
}

func (s *Server) handleReadTopic(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	parsed, err := ParseURI(req.Params.URI)
	if err != nil {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	if parsed.Kind != URIKindTopic {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}

	if _, err := s.db.GetTopicByName(ctx, parsed.Name); err != nil {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}

	msgs, err := s.db.ReadMessages(ctx, parsed.Name, parsed.Since, parsed.Limit)
	if err != nil {
		return nil, err
	}
	out := struct {
		Topic      string        `json:"topic"`
		Messages   []messageView `json:"messages"`
		NextCursor int64         `json:"next_cursor"`
		HasMore    bool          `json:"has_more"`
	}{
		Topic:    parsed.Name,
		Messages: make([]messageView, 0, len(msgs)),
	}
	for _, m := range msgs {
		out.Messages = append(out.Messages, messageView{
			ID: m.ID, AuthorName: m.AuthorName, Body: m.Body, CreatedAtUS: m.CreatedAtUS,
		})
	}
	if n := len(msgs); n > 0 {
		out.NextCursor = msgs[n-1].ID
		probe, err := s.db.ReadMessages(ctx, parsed.Name, out.NextCursor, 1)
		if err == nil && len(probe) > 0 {
			out.HasMore = true
		}
	} else {
		out.NextCursor = parsed.Since
	}
	return jsonResource(req.Params.URI, out)
}

func (s *Server) handleReadInbox(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	parsed, err := ParseURI(req.Params.URI)
	if err != nil {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	if parsed.Kind != URIKindInbox {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}

	id := s.Identity()
	if id == nil || id.Name != parsed.Name {
		return nil, fmt.Errorf("inbox is private; only the inbox owner may read it")
	}

	mentions, err := s.db.InboxFor(ctx, id.ID)
	if err != nil {
		return nil, err
	}
	type entry struct {
		MessageID   int64  `json:"message_id"`
		Topic       string `json:"topic"`
		AuthorName  string `json:"author_name"`
		Body        string `json:"body"`
		CreatedAtUS int64  `json:"created_at_us"`
	}
	out := struct {
		Mentions []entry `json:"mentions"`
	}{Mentions: make([]entry, 0, len(mentions))}
	for _, m := range mentions {
		out.Mentions = append(out.Mentions, entry{
			MessageID: m.MessageID, Topic: m.TopicName,
			AuthorName: m.AuthorName, Body: m.Body, CreatedAtUS: m.CreatedAtUS,
		})
	}
	return jsonResource(req.Params.URI, out)
}

func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(body),
		}},
	}, nil
}
