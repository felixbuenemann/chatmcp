package store

import (
	"context"
	"database/sql"
	"time"
)

type Mention struct {
	MessageID    int64
	TopicName    string
	AuthorName   string
	Body         string
	CreatedAtUS  int64
	DismissedAt  sql.NullInt64
}

const inboxCap = 1000

// InboxFor returns the recipient's inbox: undismissed mentions of the agent,
// newest first. Capped at 100 entries per call to avoid unbounded payloads.
func (db *DB) InboxFor(ctx context.Context, agentID int64) ([]Mention, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT mn.message_id, t.name, m.author_name, m.body, m.created_at_us, mn.dismissed_at
		FROM mentions mn
		JOIN messages m ON m.id = mn.message_id
		JOIN topics   t ON t.id = m.topic_id
		WHERE mn.mentioned_agent_id = ? AND mn.dismissed_at IS NULL
		ORDER BY mn.message_id DESC
		LIMIT 100
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Mention
	for rows.Next() {
		var m Mention
		if err := rows.Scan(&m.MessageID, &m.TopicName, &m.AuthorName, &m.Body, &m.CreatedAtUS, &m.DismissedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DismissInbox marks all undismissed mentions for agentID as dismissed.
func (db *DB) DismissInbox(ctx context.Context, agentID int64) (int64, error) {
	var n int64
	err := db.WithWriteLock(func() error {
		res, err := db.sql.ExecContext(ctx, `
			UPDATE mentions SET dismissed_at = ?
			WHERE mentioned_agent_id = ? AND dismissed_at IS NULL
		`, nowMicros(), agentID)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// EnforceInboxCap auto-dismisses oldest undismissed mentions for agentID
// beyond inboxCap to keep the inbox bounded.
func (db *DB) EnforceInboxCap(ctx context.Context, agentID int64) error {
	return db.WithWriteLock(func() error {
		_, err := db.sql.ExecContext(ctx, `
			UPDATE mentions SET dismissed_at = ?
			WHERE mentioned_agent_id = ? AND dismissed_at IS NULL
			  AND message_id NOT IN (
			    SELECT message_id FROM mentions
			    WHERE mentioned_agent_id = ? AND dismissed_at IS NULL
			    ORDER BY message_id DESC LIMIT ?
			  )
		`, nowMicros(), agentID, agentID, inboxCap)
		return err
	})
}

func nowMicros() int64 {
	return time.Now().UnixMicro()
}
