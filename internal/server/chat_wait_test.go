package server_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buenemann/chatmcp/internal/server"
	"github.com/buenemann/chatmcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectTwoServers opens two independent *store.DB instances against the
// same file (mirroring production: two processes, two pools) and wires each
// to its own server.New(db). PRAGMA data_version cross-pool detection only
// works reliably when reads and writes go through different pools.
func connectTwoServers(t *testing.T, ctx context.Context, clientNameA, clientNameB string) (*mcp.ClientSession, *mcp.ClientSession) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "chat.db")
	dbA, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("dbA: %v", err)
	}
	t.Cleanup(func() { dbA.Close() })
	dbB, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("dbB: %v", err)
	}
	t.Cleanup(func() { dbB.Close() })

	srvA := server.New(dbA)
	srvB := server.New(dbB)

	mkClient := func(srv *server.Server, clientName string) *mcp.ClientSession {
		ct, st := mcp.NewInMemoryTransports()
		if _, err := srv.MCP().Connect(ctx, st, nil); err != nil {
			t.Fatalf("server connect: %v", err)
		}
		c := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "test"}, nil)
		cs, err := c.Connect(ctx, ct, nil)
		if err != nil {
			t.Fatalf("client connect: %v", err)
		}
		t.Cleanup(func() { cs.Close() })
		return cs
	}
	return mkClient(srvA, clientNameA), mkClient(srvB, clientNameB)
}

func TestChatWait_ReturnsImmediatelyWhenMessageAlreadyExists(t *testing.T) {
	ctx := context.Background()
	csA, csB := connectTwoServers(t, ctx, "claude-code", "codex")

	if r := callTool(t, ctx, csA, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("alice claim")
	}
	if r := callTool(t, ctx, csB, "chat_set_name", map[string]any{"name": "bob"}); r.IsError {
		t.Fatal("bob claim")
	}
	if r := callTool(t, ctx, csA, "chat_join", map[string]any{"topic": "wait1"}); r.IsError {
		t.Fatal("join")
	}
	// bob posts BEFORE alice waits
	if r := callTool(t, ctx, csB, "chat_post", map[string]any{"topic": "wait1", "body": "hello"}); r.IsError {
		t.Fatal("post")
	}

	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	res, err := csA.CallTool(deadline, &mcp.CallToolParams{
		Name:      "chat_wait",
		Arguments: map[string]any{"topic": "wait1", "since": 0, "timeout_s": 10},
	})
	if err != nil {
		t.Fatalf("chat_wait: %v", err)
	}
	var out struct {
		Messages []struct {
			AuthorName string `json:"author_name"`
			Body       string `json:"body"`
		} `json:"messages"`
		TimedOut bool `json:"timed_out"`
	}
	if !structuredOutput(t, res, &out) {
		t.Fatal("no structured output")
	}
	if out.TimedOut {
		t.Fatal("should not time out — message exists already")
	}
	if len(out.Messages) != 1 || out.Messages[0].AuthorName != "bob" || out.Messages[0].Body != "hello" {
		t.Fatalf("unexpected messages: %+v", out.Messages)
	}
}

func TestChatWait_BlocksUntilMessageArrives(t *testing.T) {
	ctx := context.Background()
	csA, csB := connectTwoServers(t, ctx, "claude-code", "codex")

	if r := callTool(t, ctx, csA, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("alice claim")
	}
	if r := callTool(t, ctx, csB, "chat_set_name", map[string]any{"name": "bob"}); r.IsError {
		t.Fatal("bob claim")
	}
	if r := callTool(t, ctx, csA, "chat_join", map[string]any{"topic": "wait2"}); r.IsError {
		t.Fatal("join")
	}

	// alice waits; bob posts after a short delay; alice should wake up.
	go func() {
		time.Sleep(400 * time.Millisecond)
		_, _ = csB.CallTool(ctx, &mcp.CallToolParams{
			Name:      "chat_post",
			Arguments: map[string]any{"topic": "wait2", "body": "wake up"},
		})
	}()

	start := time.Now()
	res, err := csA.CallTool(ctx, &mcp.CallToolParams{
		Name:      "chat_wait",
		Arguments: map[string]any{"topic": "wait2", "since": 0, "timeout_s": 5},
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("chat_wait: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("chat_wait should have returned ~500ms after start, took %v", elapsed)
	}
	var out struct {
		Messages []struct{ Body string } `json:"messages"`
		TimedOut bool                    `json:"timed_out"`
	}
	if !structuredOutput(t, res, &out) {
		t.Fatal("no output")
	}
	if out.TimedOut {
		t.Fatal("expected message not timeout")
	}
	if len(out.Messages) != 1 || out.Messages[0].Body != "wake up" {
		t.Fatalf("unexpected: %+v", out)
	}
}

func TestChatWait_SuppressesSelfEcho(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "claude-code")

	if r := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("claim")
	}
	if r := callTool(t, ctx, cs, "chat_join", map[string]any{"topic": "wait3"}); r.IsError {
		t.Fatal("join")
	}
	// alice posts her own message
	if r := callTool(t, ctx, cs, "chat_post", map[string]any{"topic": "wait3", "body": "self"}); r.IsError {
		t.Fatal("post")
	}

	// chat_wait should NOT return alice's own post; should time out.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "chat_wait",
		Arguments: map[string]any{"topic": "wait3", "since": 0, "timeout_s": 1},
	})
	if err != nil {
		t.Fatalf("chat_wait: %v", err)
	}
	var out struct {
		Messages []struct{} `json:"messages"`
		TimedOut bool       `json:"timed_out"`
	}
	if !structuredOutput(t, res, &out) {
		t.Fatal("no output")
	}
	if !out.TimedOut {
		t.Fatal("should have timed out — only own message exists")
	}
	if len(out.Messages) != 0 {
		t.Fatalf("expected zero messages, got %d", len(out.Messages))
	}
}

func TestChatWait_TimeoutFiresWithEmptyResult(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "claude-code")
	if r := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("claim")
	}
	if r := callTool(t, ctx, cs, "chat_join", map[string]any{"topic": "wait4"}); r.IsError {
		t.Fatal("join")
	}

	start := time.Now()
	res, _ := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "chat_wait",
		Arguments: map[string]any{"topic": "wait4", "since": 0, "timeout_s": 1},
	})
	elapsed := time.Since(start)
	if elapsed < 800*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Errorf("expected ~1s wait, got %v", elapsed)
	}
	var out struct {
		Messages []struct{} `json:"messages"`
		TimedOut bool       `json:"timed_out"`
	}
	if !structuredOutput(t, res, &out) {
		t.Fatal("no output")
	}
	if !out.TimedOut || len(out.Messages) != 0 {
		t.Fatalf("expected empty timed-out result, got %+v", out)
	}
}

func TestChatWait_RequiresIdentity(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "claude-code")

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "chat_wait",
		Arguments: map[string]any{"topic": "x", "timeout_s": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected IsError before claim")
	}
}

func TestChatWait_HostMaxClampingDoesNotExceed(t *testing.T) {
	// We can't easily verify the description re-registration flow without
	// rolling our own initialize; verify the clamping numerically by passing
	// a huge timeout and ensuring the call returns within a bounded window
	// when the unknown-host clamp (25s) would be in effect.
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "test-client") // unknown kind → 25s clamp
	if r := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("claim")
	}
	if r := callTool(t, ctx, cs, "chat_join", map[string]any{"topic": "wait5"}); r.IsError {
		t.Fatal("join")
	}

	// Request 1 hour; expect clamp to ~25s. Run with 30s deadline so a
	// regression (no clamp) trips a timeout we can detect.
	var done int64
	go func() {
		_, _ = cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "chat_wait",
			Arguments: map[string]any{"topic": "wait5", "since": 0, "timeout_s": 3600},
		})
		atomic.StoreInt64(&done, 1)
	}()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&done) == 1 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("chat_wait did not return within 30s — clamp likely broken")
}
