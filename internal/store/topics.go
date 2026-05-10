package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"
)

type Topic struct {
	ID            int64
	Name          string
	CreatedBy     int64
	CreatedAt     int64
	MemberCount   int
	LastMessageAt int64 // 0 if no messages
}

type Member struct {
	AgentID  int64
	Name     string
	Kind     string
	JoinedAt int64
}

var topicNameRegexp = regexp.MustCompile(`^[a-zA-Z0-9._/-]{1,64}$`)

func ValidateTopicName(name string) bool {
	return topicNameRegexp.MatchString(name)
}

// JoinTopic finds-or-creates the topic and ensures agentID is a member.
// Returns the topic and its current members. Idempotent.
func (db *DB) JoinTopic(ctx context.Context, topicName string, agentID int64) (*Topic, []Member, error) {
	if !ValidateTopicName(topicName) {
		return nil, nil, errors.New("invalid topic name")
	}

	var topic *Topic
	err := db.WithWriteLock(func() error {
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		var t Topic
		err = tx.QueryRowContext(ctx,
			"SELECT id, name, created_by, created_at FROM topics WHERE name = ?",
			topicName,
		).Scan(&t.ID, &t.Name, &t.CreatedBy, &t.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			now := time.Now().UnixMicro()
			res, err := tx.ExecContext(ctx,
				"INSERT INTO topics (name, created_by, created_at) VALUES (?, ?, ?)",
				topicName, agentID, now,
			)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			t = Topic{ID: id, Name: topicName, CreatedBy: agentID, CreatedAt: now}
		} else if err != nil {
			return err
		}

		// Insert membership idempotently.
		now := time.Now().UnixMicro()
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO memberships (topic_id, agent_id, joined_at) VALUES (?, ?, ?)",
			t.ID, agentID, now,
		); err != nil {
			return err
		}

		topic = &t
		return tx.Commit()
	})
	if err != nil {
		return nil, nil, err
	}

	members, err := db.ListMembers(ctx, topic.ID)
	if err != nil {
		return nil, nil, err
	}
	return topic, members, nil
}

// LeaveTopic removes agentID from topicName. Idempotent.
func (db *DB) LeaveTopic(ctx context.Context, topicName string, agentID int64) error {
	return db.WithWriteLock(func() error {
		_, err := db.sql.ExecContext(ctx,
			`DELETE FROM memberships
			 WHERE agent_id = ? AND topic_id = (SELECT id FROM topics WHERE name = ?)`,
			agentID, topicName,
		)
		return err
	})
}

// GetTopicByName returns the topic row for the given name, or sql.ErrNoRows.
func (db *DB) GetTopicByName(ctx context.Context, name string) (*Topic, error) {
	var t Topic
	err := db.sql.QueryRowContext(ctx,
		"SELECT id, name, created_by, created_at FROM topics WHERE name = ?",
		name,
	).Scan(&t.ID, &t.Name, &t.CreatedBy, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListTopics returns all topics with member counts and last message timestamps.
func (db *DB) ListTopics(ctx context.Context) ([]Topic, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT t.id, t.name, t.created_by, t.created_at,
		       (SELECT COUNT(*) FROM memberships m WHERE m.topic_id = t.id) AS member_count,
		       COALESCE((SELECT MAX(created_at_us) FROM messages WHERE topic_id = t.id), 0) AS last_msg
		FROM topics t
		ORDER BY t.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Topic
	for rows.Next() {
		var t Topic
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedBy, &t.CreatedAt, &t.MemberCount, &t.LastMessageAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListMembers returns all agents who are members of the given topic.
func (db *DB) ListMembers(ctx context.Context, topicID int64) ([]Member, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT a.id, a.name, a.kind, m.joined_at
		FROM memberships m JOIN agents a ON a.id = m.agent_id
		WHERE m.topic_id = ?
		ORDER BY m.joined_at
	`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.AgentID, &m.Name, &m.Kind, &m.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
