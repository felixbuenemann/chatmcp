package server

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/buenemann/chatmcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- types

type topicSummary struct {
	Name          string `json:"name"`
	MemberCount   int    `json:"member_count"`
	LastMessageAt int64  `json:"last_message_at"`
}

type memberView struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	JoinedAt int64  `json:"joined_at"`
}

type messageView struct {
	ID          int64  `json:"id"`
	AuthorName  string `json:"author_name"`
	Body        string `json:"body"`
	CreatedAtUS int64  `json:"created_at_us"`
}

// --- chat_list_topics

type ListTopicsInput struct{}
type ListTopicsOutput struct {
	Topics []topicSummary `json:"topics"`
}

func (s *Server) handleListTopics(ctx context.Context, _ *mcp.CallToolRequest, _ ListTopicsInput) (*mcp.CallToolResult, ListTopicsOutput, error) {
	if _, err := s.requireIdentity(); err != nil {
		return errToolResult("%v", err), ListTopicsOutput{}, nil
	}
	rows, err := s.db.ListTopics(ctx)
	if err != nil {
		return nil, ListTopicsOutput{}, err
	}
	out := ListTopicsOutput{Topics: make([]topicSummary, 0, len(rows))}
	for _, t := range rows {
		out.Topics = append(out.Topics, topicSummary{
			Name: t.Name, MemberCount: t.MemberCount, LastMessageAt: t.LastMessageAt,
		})
	}
	return nil, out, nil
}

// --- chat_join

type JoinInput struct {
	Topic string `json:"topic" jsonschema:"topic name without leading hash"`
}
type JoinOutput struct {
	Topic   string       `json:"topic"`
	Members []memberView `json:"members"`
}

func (s *Server) handleJoin(ctx context.Context, _ *mcp.CallToolRequest, in JoinInput) (*mcp.CallToolResult, JoinOutput, error) {
	id, err := s.requireIdentity()
	if err != nil {
		return errToolResult("%v", err), JoinOutput{}, nil
	}
	if !store.ValidateTopicName(in.Topic) {
		return errToolResult("invalid topic name (must match [a-zA-Z0-9._/-]{1,64})"), JoinOutput{}, nil
	}
	t, members, err := s.db.JoinTopic(ctx, in.Topic, id.ID)
	if err != nil {
		return nil, JoinOutput{}, err
	}
	out := JoinOutput{Topic: t.Name, Members: toMemberViews(members)}
	return nil, out, nil
}

// --- chat_leave

type LeaveInput struct {
	Topic string `json:"topic"`
}
type LeaveOutput struct {
	OK bool `json:"ok"`
}

func (s *Server) handleLeave(ctx context.Context, _ *mcp.CallToolRequest, in LeaveInput) (*mcp.CallToolResult, LeaveOutput, error) {
	id, err := s.requireIdentity()
	if err != nil {
		return errToolResult("%v", err), LeaveOutput{}, nil
	}
	if err := s.db.LeaveTopic(ctx, in.Topic, id.ID); err != nil {
		return nil, LeaveOutput{}, err
	}
	return nil, LeaveOutput{OK: true}, nil
}

// --- chat_post

type PostInput struct {
	Topic string `json:"topic"`
	Body  string `json:"body" jsonschema:"message body; @name mentions are parsed and delivered to the named agent's inbox"`
}
type PostOutput struct {
	MessageID   int64    `json:"message_id"`
	CreatedAtUS int64    `json:"created_at_us"`
	Mentions    []string `json:"mentions,omitempty"`
}

func (s *Server) handlePost(ctx context.Context, _ *mcp.CallToolRequest, in PostInput) (*mcp.CallToolResult, PostOutput, error) {
	id, err := s.requireIdentity()
	if err != nil {
		return errToolResult("%v", err), PostOutput{}, nil
	}
	if !store.ValidateTopicName(in.Topic) {
		return errToolResult("invalid topic name (must match [a-zA-Z0-9._/-]{1,64})"), PostOutput{}, nil
	}
	if in.Body == "" {
		return errToolResult("message body must not be empty"), PostOutput{}, nil
	}
	r, err := s.db.PostMessage(ctx, in.Topic, id, in.Body)
	if err != nil {
		return nil, PostOutput{}, err
	}
	// Best-effort: bound each mentioned agent's unread inbox to prevent runaway
	// loops between agents. Errors here don't block the post.
	for _, aid := range r.MentionedIDs {
		_ = s.db.EnforceInboxCap(ctx, aid)
	}
	return nil, PostOutput{
		MessageID:   r.Message.ID,
		CreatedAtUS: r.Message.CreatedAtUS,
		Mentions:    r.Mentions,
	}, nil
}

// --- chat_read

type ReadInput struct {
	Topic string `json:"topic"`
	Since int64  `json:"since,omitempty" jsonschema:"only return messages with id greater than this; use 0 to start from the beginning"`
	Limit int    `json:"limit,omitempty" jsonschema:"max messages to return; default 50, hard cap 500"`
}
type ReadOutput struct {
	Messages   []messageView `json:"messages"`
	NextCursor int64         `json:"next_cursor"`
	HasMore    bool          `json:"has_more"`
}

func (s *Server) handleRead(ctx context.Context, _ *mcp.CallToolRequest, in ReadInput) (*mcp.CallToolResult, ReadOutput, error) {
	if _, err := s.requireIdentity(); err != nil {
		return errToolResult("%v", err), ReadOutput{}, nil
	}
	if _, err := s.db.GetTopicByName(ctx, in.Topic); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errToolResult("topic %q does not exist", in.Topic), ReadOutput{}, nil
		}
		return nil, ReadOutput{}, err
	}
	msgs, err := s.db.ReadMessages(ctx, in.Topic, in.Since, in.Limit)
	if err != nil {
		return nil, ReadOutput{}, err
	}

	out := ReadOutput{Messages: make([]messageView, 0, len(msgs))}
	for _, m := range msgs {
		out.Messages = append(out.Messages, messageView{
			ID: m.ID, AuthorName: m.AuthorName, Body: m.Body, CreatedAtUS: m.CreatedAtUS,
		})
	}
	if n := len(msgs); n > 0 {
		out.NextCursor = msgs[n-1].ID
		// Probe for one extra row to set HasMore — cheap.
		probe, err := s.db.ReadMessages(ctx, in.Topic, out.NextCursor, 1)
		if err == nil && len(probe) > 0 {
			out.HasMore = true
		}
	} else {
		out.NextCursor = in.Since
	}
	return nil, out, nil
}

// --- chat_who

type WhoInput struct {
	Topic string `json:"topic"`
}
type WhoOutput struct {
	Members []memberView `json:"members"`
}

func (s *Server) handleWho(ctx context.Context, _ *mcp.CallToolRequest, in WhoInput) (*mcp.CallToolResult, WhoOutput, error) {
	if _, err := s.requireIdentity(); err != nil {
		return errToolResult("%v", err), WhoOutput{}, nil
	}
	t, err := s.db.GetTopicByName(ctx, in.Topic)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errToolResult("topic %q does not exist", in.Topic), WhoOutput{}, nil
		}
		return nil, WhoOutput{}, err
	}
	members, err := s.db.ListMembers(ctx, t.ID)
	if err != nil {
		return nil, WhoOutput{}, err
	}
	return nil, WhoOutput{Members: toMemberViews(members)}, nil
}

// --- chat_wait

type WaitInput struct {
	Topic    string `json:"topic"`
	Since    int64  `json:"since,omitempty" jsonschema:"only return messages with id greater than this; 0 returns from the beginning of the topic"`
	TimeoutS int    `json:"timeout_s,omitempty" jsonschema:"how long to block in seconds; default 25; server clamps to host max"`
}
type WaitOutput struct {
	Messages   []messageView `json:"messages"`
	NextCursor int64         `json:"next_cursor"`
	TimedOut   bool          `json:"timed_out,omitempty"`
}

func (s *Server) handleChatWait(ctx context.Context, req *mcp.CallToolRequest, in WaitInput) (*mcp.CallToolResult, WaitOutput, error) {
	id, err := s.requireIdentity()
	if err != nil {
		return errToolResult("%v", err), WaitOutput{}, nil
	}
	if !store.ValidateTopicName(in.Topic) {
		return errToolResult("invalid topic name (must match [a-zA-Z0-9._/-]{1,64})"), WaitOutput{}, nil
	}
	if _, err := s.db.GetTopicByName(ctx, in.Topic); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errToolResult("topic %q does not exist; chat_join it first", in.Topic), WaitOutput{}, nil
		}
		return nil, WaitOutput{}, err
	}

	kind := clientInfoName(req)
	timeoutS := in.TimeoutS
	if timeoutS <= 0 {
		timeoutS = 25
	}
	if maxS := hostSafeMaxTimeoutS(kind); timeoutS > maxS {
		timeoutS = maxS
	}

	progressToken := req.Params.GetProgressToken()

	// Immediate check: if there are already new messages from others, return
	// without entering the poll loop.
	if msgs, err := s.fetchNewForWait(ctx, in.Topic, in.Since, id.ID); err != nil {
		return nil, WaitOutput{}, err
	} else if len(msgs) > 0 {
		return nil, buildWaitOutput(msgs, in.Since, false), nil
	}

	deadline := time.After(time.Duration(timeoutS) * time.Second)
	pollTick := time.NewTicker(200 * time.Millisecond)
	defer pollTick.Stop()
	progressTick := time.NewTicker(5 * time.Second)
	defer progressTick.Stop()

	startedAt := time.Now()
	var lastDV int64

	for {
		select {
		case <-ctx.Done():
			// Host cancelled the call — return empty cleanly. The agent
			// likely won't see this since the host abandoned the response,
			// but we still close out the loop.
			return nil, WaitOutput{NextCursor: in.Since}, nil

		case <-deadline:
			return nil, WaitOutput{NextCursor: in.Since, TimedOut: true}, nil

		case <-progressTick.C:
			if progressToken != nil && req.Session != nil {
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: progressToken,
					Progress:      time.Since(startedAt).Seconds(),
					Total:         float64(timeoutS),
					Message:       "chat_wait: no new messages yet",
				})
			}

		case <-pollTick.C:
			dv, err := s.db.DataVersion(ctx)
			if err != nil {
				return nil, WaitOutput{}, err
			}
			if dv == lastDV {
				continue
			}
			lastDV = dv
			msgs, err := s.fetchNewForWait(ctx, in.Topic, in.Since, id.ID)
			if err != nil {
				return nil, WaitOutput{}, err
			}
			if len(msgs) == 0 {
				continue
			}
			return nil, buildWaitOutput(msgs, in.Since, false), nil
		}
	}
}

func (s *Server) fetchNewForWait(ctx context.Context, topic string, since, selfID int64) ([]store.Message, error) {
	return s.db.ReadNewMessagesFromOthers(ctx, topic, since, selfID, 50)
}

func buildWaitOutput(msgs []store.Message, since int64, timedOut bool) WaitOutput {
	out := WaitOutput{
		Messages: make([]messageView, 0, len(msgs)),
		TimedOut: timedOut,
	}
	for _, m := range msgs {
		out.Messages = append(out.Messages, messageView{
			ID: m.ID, AuthorName: m.AuthorName, Body: m.Body, CreatedAtUS: m.CreatedAtUS,
		})
	}
	if n := len(msgs); n > 0 {
		out.NextCursor = msgs[n-1].ID
	} else {
		out.NextCursor = since
	}
	return out
}

// --- chat_inbox

type InboxInput struct {
	Dismiss bool `json:"dismiss,omitempty" jsonschema:"if true mark all currently-listed mentions as read after returning them"`
}
type InboxEntry struct {
	MessageID   int64  `json:"message_id"`
	Topic       string `json:"topic"`
	AuthorName  string `json:"author_name"`
	Body        string `json:"body"`
	CreatedAtUS int64  `json:"created_at_us"`
}
type InboxOutput struct {
	Mentions    []InboxEntry `json:"mentions"`
	Dismissed   int64        `json:"dismissed,omitempty"`
}

func (s *Server) handleInbox(ctx context.Context, _ *mcp.CallToolRequest, in InboxInput) (*mcp.CallToolResult, InboxOutput, error) {
	id, err := s.requireIdentity()
	if err != nil {
		return errToolResult("%v", err), InboxOutput{}, nil
	}
	mentions, err := s.db.InboxFor(ctx, id.ID)
	if err != nil {
		return nil, InboxOutput{}, err
	}
	out := InboxOutput{Mentions: make([]InboxEntry, 0, len(mentions))}
	for _, m := range mentions {
		out.Mentions = append(out.Mentions, InboxEntry{
			MessageID: m.MessageID, Topic: m.TopicName,
			AuthorName: m.AuthorName, Body: m.Body, CreatedAtUS: m.CreatedAtUS,
		})
	}
	if in.Dismiss {
		n, err := s.db.DismissInbox(ctx, id.ID)
		if err != nil {
			return nil, InboxOutput{}, err
		}
		out.Dismissed = n
	}
	return nil, out, nil
}

// helpers

func toMemberViews(in []store.Member) []memberView {
	out := make([]memberView, 0, len(in))
	for _, m := range in {
		out = append(out, memberView{Name: m.Name, Kind: m.Kind, JoinedAt: m.JoinedAt})
	}
	return out
}
