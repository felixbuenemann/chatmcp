package notifier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/buenemann/chatmcp/internal/server"
	"github.com/buenemann/chatmcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Notifier polls the shared SQLite DB for changes committed by other chatmcp
// processes and pushes mcp.ResourceUpdated notifications to subscribed sessions
// of the local mcp.Server. It is the cross-process wake-up mechanism — see
// the design plan for self-echo and idle-backoff semantics.
type Notifier struct {
	srv          *server.Server
	interval     time.Duration
	jitterFrac   float64       // ±20% by default
	idleBackoff  time.Duration // applied after idleAfterTicks quiet ticks
	idleAfter    int

	lastDataVersion int64
	lastMessageID   int64
	lastTopicID     int64
}

func New(srv *server.Server, interval time.Duration) *Notifier {
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	return &Notifier{
		srv:         srv,
		interval:    interval,
		jitterFrac:  0.20,
		idleBackoff: time.Second,
		idleAfter:   50,
	}
}

func (n *Notifier) Run(ctx context.Context) error {
	if err := n.initWatermarks(ctx); err != nil {
		return fmt.Errorf("init watermarks: %w", err)
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	idleTicks := 0
	for {
		sleep := n.interval
		if idleTicks >= n.idleAfter {
			sleep = n.idleBackoff
		}
		// Apply ±jitterFrac jitter.
		jitter := (rng.Float64()*2 - 1) * n.jitterFrac
		sleep = time.Duration(float64(sleep) * (1 + jitter))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}

		fired, err := n.tick(ctx)
		if err != nil {
			// Log to stderr (server logs go to stderr; stdout is JSON-RPC) and continue.
			fmt.Fprintf(os.Stderr, "chatmcp: notifier tick error: %v\n", err)
			continue
		}
		if fired > 0 {
			idleTicks = 0
		} else {
			idleTicks++
		}
	}
}

// tick reads the current data_version; if it advanced, queries for new messages
// and mentions and emits ResourceUpdated for each affected URI. Returns the
// number of notifications sent.
func (n *Notifier) tick(ctx context.Context) (int, error) {
	v, err := n.srv.DB().DataVersion(ctx)
	if err != nil {
		return 0, err
	}
	if v == n.lastDataVersion {
		return 0, nil
	}
	n.lastDataVersion = v

	selfID := int64(0)
	if id := n.srv.Identity(); id != nil {
		selfID = id.ID
	}

	fired := 0

	topicNames, lastMsgID, err := queryAffectedTopics(ctx, n.srv.DB(), n.lastMessageID, selfID)
	if err != nil {
		return fired, err
	}
	for _, name := range topicNames {
		if err := n.srv.MCP().ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{
			URI: server.TopicURI(name),
		}); err != nil {
			return fired, err
		}
		fired++
	}

	inboxOwners, _, err := queryAffectedInboxes(ctx, n.srv.DB(), n.lastMessageID, selfID)
	if err != nil {
		return fired, err
	}
	for _, owner := range inboxOwners {
		if err := n.srv.MCP().ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{
			URI: server.InboxURI(owner),
		}); err != nil {
			return fired, err
		}
		fired++
	}

	if lastMsgID > n.lastMessageID {
		n.lastMessageID = lastMsgID
	}

	// Topics list: refresh on any new topics row, regardless of authorship.
	topicListChanged, latestTopicID, err := queryNewTopics(ctx, n.srv.DB(), n.lastTopicID)
	if err != nil {
		return fired, err
	}
	if topicListChanged {
		if err := n.srv.MCP().ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{
			URI: server.URITopics,
		}); err != nil {
			return fired, err
		}
		fired++
	}
	if latestTopicID > n.lastTopicID {
		n.lastTopicID = latestTopicID
	}

	return fired, nil
}

func (n *Notifier) initWatermarks(ctx context.Context) error {
	v, err := n.srv.DB().DataVersion(ctx)
	if err != nil {
		return err
	}
	n.lastDataVersion = v

	if err := n.srv.DB().Conn().QueryRowContext(ctx,
		"SELECT COALESCE(MAX(id), 0) FROM messages",
	).Scan(&n.lastMessageID); err != nil {
		return err
	}
	if err := n.srv.DB().Conn().QueryRowContext(ctx,
		"SELECT COALESCE(MAX(id), 0) FROM topics",
	).Scan(&n.lastTopicID); err != nil {
		return err
	}
	return nil
}

func queryAffectedTopics(ctx context.Context, db *store.DB, lastID, selfID int64) ([]string, int64, error) {
	rows, err := db.Conn().QueryContext(ctx, `
		SELECT t.name, MAX(m.id) AS max_id
		FROM messages m JOIN topics t ON t.id = m.topic_id
		WHERE m.id > ? AND m.author_id != ?
		GROUP BY t.name
	`, lastID, selfID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var names []string
	var maxID int64
	for rows.Next() {
		var name string
		var rowMax int64
		if err := rows.Scan(&name, &rowMax); err != nil {
			return nil, 0, err
		}
		names = append(names, name)
		if rowMax > maxID {
			maxID = rowMax
		}
	}
	return names, maxID, rows.Err()
}

func queryAffectedInboxes(ctx context.Context, db *store.DB, lastID, selfID int64) ([]string, int64, error) {
	rows, err := db.Conn().QueryContext(ctx, `
		SELECT DISTINCT a.name
		FROM mentions mn
		JOIN messages m ON m.id = mn.message_id
		JOIN agents a ON a.id = mn.mentioned_agent_id
		WHERE m.id > ? AND m.author_id != ?
	`, lastID, selfID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, 0, err
		}
		names = append(names, name)
	}
	return names, 0, rows.Err()
}

func queryNewTopics(ctx context.Context, db *store.DB, lastID int64) (bool, int64, error) {
	var maxID int64
	err := db.Conn().QueryRowContext(ctx,
		"SELECT COALESCE(MAX(id), 0) FROM topics",
	).Scan(&maxID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, 0, err
	}
	return maxID > lastID, maxID, nil
}
