package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"
)

type Message struct {
	ID          int64
	TopicID     int64
	TopicName   string
	AuthorID    int64
	AuthorName  string
	Body        string
	CreatedAtUS int64
}

type PostResult struct {
	Message     *Message
	Mentions    []string // names of agents matched & inserted into mentions table
	MentionedIDs []int64 // agent_ids parallel to Mentions, for inbox-cap enforcement
}

var mentionRegexp = regexp.MustCompile(`@([a-zA-Z0-9._-]+)`)

// ParseMentionCandidates extracts @-prefixed names from body. Returned in order
// of first occurrence; deduplicated.
func ParseMentionCandidates(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range mentionRegexp.FindAllStringSubmatch(body, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// PostMessage inserts a message into the topic, parses @mentions, and inserts
// rows into the mentions table for each matched live agent name (excluding the
// author's own name — self-mentions are not delivered).
//
// The author is auto-joined to the topic as a side effect of posting (we
// require posting agents to be members for a clean membership model).
func (db *DB) PostMessage(ctx context.Context, topicName string, author *Agent, body string) (*PostResult, error) {
	if !ValidateTopicName(topicName) {
		return nil, errors.New("invalid topic name")
	}

	mentionNames := ParseMentionCandidates(body)
	var msg *Message
	var matchedMentions []string
	var matchedIDs []int64

	err := db.WithWriteLock(func() error {
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		topicID, err := findOrCreateTopicTx(ctx, tx, topicName, author.ID)
		if err != nil {
			return err
		}

		// Auto-join the author so chat_who reflects current participants.
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO memberships (topic_id, agent_id, joined_at) VALUES (?, ?, ?)",
			topicID, author.ID, time.Now().UnixMicro(),
		); err != nil {
			return err
		}

		now := time.Now().UnixMicro()
		res, err := tx.ExecContext(ctx,
			`INSERT INTO messages (topic_id, author_id, author_name, body, created_at_us)
			 VALUES (?, ?, ?, ?, ?)`,
			topicID, author.ID, author.Name, body, now,
		)
		if err != nil {
			return err
		}
		mid, _ := res.LastInsertId()

		msg = &Message{
			ID: mid, TopicID: topicID, TopicName: topicName,
			AuthorID: author.ID, AuthorName: author.Name,
			Body: body, CreatedAtUS: now,
		}

		// Resolve and persist mentions (excluding self).
		for _, name := range mentionNames {
			if name == author.Name {
				continue
			}
			var mentionedID int64
			err := tx.QueryRowContext(ctx,
				"SELECT id FROM agents WHERE name = ?", name,
			).Scan(&mentionedID)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO mentions (message_id, mentioned_agent_id) VALUES (?, ?)`,
				mid, mentionedID,
			); err != nil {
				return err
			}
			matchedMentions = append(matchedMentions, name)
			matchedIDs = append(matchedIDs, mentionedID)
		}

		return tx.Commit()
	})
	if err != nil {
		return nil, err
	}
	return &PostResult{Message: msg, Mentions: matchedMentions, MentionedIDs: matchedIDs}, nil
}

func findOrCreateTopicTx(ctx context.Context, tx *sql.Tx, name string, createdBy int64) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM topics WHERE name = ?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		"INSERT INTO topics (name, created_by, created_at) VALUES (?, ?, ?)",
		name, createdBy, time.Now().UnixMicro(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ReadMessages returns messages in topicName with id > since, oldest first,
// up to limit messages. limit is capped to maxLimit. Caller can paginate by
// passing the last returned id as `since` on the next call.
func (db *DB) ReadMessages(ctx context.Context, topicName string, since int64, limit int) ([]Message, error) {
	return db.readMessagesImpl(ctx, topicName, since, 0, limit)
}

// ReadNewMessagesFromOthers is ReadMessages with an additional filter that
// excludes messages authored by excludeAuthorID. Used by chat_wait to avoid
// returning the caller's own posts (self-echo suppression at the wait surface).
func (db *DB) ReadNewMessagesFromOthers(ctx context.Context, topicName string, since, excludeAuthorID int64, limit int) ([]Message, error) {
	return db.readMessagesImpl(ctx, topicName, since, excludeAuthorID, limit)
}

func (db *DB) readMessagesImpl(ctx context.Context, topicName string, since, excludeAuthorID int64, limit int) ([]Message, error) {
	const (
		defaultLimit = 50
		maxLimit     = 500
	)
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	rows, err := db.sql.QueryContext(ctx, `
		SELECT m.id, m.topic_id, t.name, m.author_id, m.author_name, m.body, m.created_at_us
		FROM messages m JOIN topics t ON t.id = m.topic_id
		WHERE t.name = ? AND m.id > ? AND (? = 0 OR m.author_id != ?)
		ORDER BY m.id ASC
		LIMIT ?
	`, topicName, since, excludeAuthorID, excludeAuthorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.TopicID, &m.TopicName, &m.AuthorID, &m.AuthorName, &m.Body, &m.CreatedAtUS); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
