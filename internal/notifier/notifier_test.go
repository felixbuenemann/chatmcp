package notifier_test

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buenemann/chatmcp/internal/notifier"
	"github.com/buenemann/chatmcp/internal/server"
	"github.com/buenemann/chatmcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestNotifier_TopicAndInboxPush spins up two chatmcp servers sharing one DB
// (simulating two agent processes). One subscribes to a topic + its inbox; the
// other posts a message that mentions the first. The first should receive both
// notifications/resources/updated within the test timeout, while the poster
// (B) should receive none (self-echo suppression).
func TestNotifier_TopicAndInboxPush(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dbPath := filepath.Join(t.TempDir(), "chat.db")

	// Process A: alice subscribes; expects pushes.
	dbA, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	srvA := server.New(dbA)
	go func() { _ = notifier.New(srvA, 50*time.Millisecond).Run(ctx) }()

	// Process B: bob posts.
	dbB, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	srvB := server.New(dbB)
	go func() { _ = notifier.New(srvB, 50*time.Millisecond).Run(ctx) }()

	// Counters for received updates.
	var topicUpdatesA, inboxUpdatesA, anyUpdatesB int64
	csA := installRecorder(t, ctx, srvA, &topicUpdatesA, &inboxUpdatesA, "claude-code-A")
	csB := installRecorder(t, ctx, srvB, nil, nil, "codex-B", &anyUpdatesB)

	// Claim names.
	if r, _ := csA.CallTool(ctx, &mcp.CallToolParams{Name: "chat_set_name", Arguments: map[string]any{"name": "alice"}}); r.IsError {
		t.Fatalf("alice claim: %+v", r)
	}
	if r, _ := csB.CallTool(ctx, &mcp.CallToolParams{Name: "chat_set_name", Arguments: map[string]any{"name": "bob"}}); r.IsError {
		t.Fatalf("bob claim: %+v", r)
	}

	// Alice subscribes.
	if err := csA.Subscribe(ctx, &mcp.SubscribeParams{URI: "chatmcp://topic/test"}); err != nil {
		t.Fatalf("subscribe topic: %v", err)
	}
	if err := csA.Subscribe(ctx, &mcp.SubscribeParams{URI: "chatmcp://inbox/alice"}); err != nil {
		t.Fatalf("subscribe inbox: %v", err)
	}
	// Bob also subscribes to the topic — must NOT receive a push for his own post.
	if err := csB.Subscribe(ctx, &mcp.SubscribeParams{URI: "chatmcp://topic/test"}); err != nil {
		t.Fatalf("bob subscribe topic: %v", err)
	}

	// Bob posts.
	if r, err := csB.CallTool(ctx, &mcp.CallToolParams{
		Name: "chat_post", Arguments: map[string]any{"topic": "test", "body": "hi @alice"},
	}); err != nil || r.IsError {
		t.Fatalf("post: err=%v res=%+v", err, r)
	}

	// Wait for at least 1 topic + 1 inbox push to alice (poll up to 2s).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&topicUpdatesA) >= 1 && atomic.LoadInt64(&inboxUpdatesA) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&topicUpdatesA); got < 1 {
		t.Errorf("alice should have received a topic update; got %d", got)
	}
	if got := atomic.LoadInt64(&inboxUpdatesA); got < 1 {
		t.Errorf("alice should have received an inbox update; got %d", got)
	}

	// Give the system 250ms more to surface any spurious self-echo to Bob.
	time.Sleep(250 * time.Millisecond)
	if got := atomic.LoadInt64(&anyUpdatesB); got != 0 {
		t.Errorf("bob (poster) should not receive any topic update for his own post (self-echo suppression); got %d", got)
	}
}

// installRecorder connects a client to the given server and wires per-URI counters via
// ClientOptions.ResourceUpdatedHandler. The first two int64 pointers are for
// topic-update and inbox-update counters (counted by URI prefix). Additional
// pointers count any update.
func installRecorder(t *testing.T, ctx context.Context, srv *server.Server, topicCnt, inboxCnt *int64, clientName string, anyCnt ...*int64) *mcp.ClientSession {
	t.Helper()

	var mu sync.Mutex
	handler := func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
		mu.Lock()
		defer mu.Unlock()
		uri := req.Params.URI
		switch {
		case len(anyCnt) > 0:
			atomic.AddInt64(anyCnt[0], 1)
		}
		if topicCnt != nil && uri == "chatmcp://topic/test" {
			atomic.AddInt64(topicCnt, 1)
		}
		if inboxCnt != nil && uri == "chatmcp://inbox/alice" {
			atomic.AddInt64(inboxCnt, 1)
		}
	}

	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.MCP().Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "test"}, &mcp.ClientOptions{
		ResourceUpdatedHandler: handler,
	})
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}
