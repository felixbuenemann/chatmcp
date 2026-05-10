package main_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestE2E_TwoSubprocessesPushAcrossOSBoundary is the only test in the suite
// that exercises chatmcp as actual OS subprocesses talking JSON-RPC over real
// stdio pipes — not the in-memory transport. It's the closest thing to running
// chatmcp from Claude Code + Codex CLI for real, without involving those clients.
//
// Two chatmcp processes share one SQLite WAL file. Process A's client subscribes
// to a topic and inbox; process B's client posts a mention. We assert A receives
// both notifications/resources/updated within a few seconds, and B (the poster)
// receives none (self-echo suppression).
func TestE2E_TwoSubprocessesPushAcrossOSBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess smoke test; skip under -short")
	}

	bin := buildBinary(t)
	dbPath := filepath.Join(t.TempDir(), "chat.db")
	env := func(name string) []string {
		return append(os.Environ(),
			"CHATMCP_DB_PATH="+dbPath,
			"CHATMCP_AGENT_NAME="+name,
			"CHATMCP_POLL_MS=50",
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// --- counters
	var aliceTopicHits, aliceInboxHits, bobAnyHits int64

	makeClient := func(clientName string, recordTopic, recordInbox bool, anyCounter *int64) (*mcp.ClientSession, *exec.Cmd) {
		cmd := exec.Command(bin)
		cmd.Env = env(clientName)
		// chatmcp writes startup logs to stderr; pipe to test log for diagnosis.
		cmd.Stderr = testWriter{t: t, prefix: clientName}

		c := mcp.NewClient(
			&mcp.Implementation{Name: clientName, Version: "test"},
			&mcp.ClientOptions{
				ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
					if anyCounter != nil {
						atomic.AddInt64(anyCounter, 1)
					}
					if recordTopic && req.Params.URI == "chatmcp://topic/test" {
						atomic.AddInt64(&aliceTopicHits, 1)
					}
					if recordInbox && req.Params.URI == "chatmcp://inbox/alice" {
						atomic.AddInt64(&aliceInboxHits, 1)
					}
				},
			},
		)

		cs, err := c.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
		if err != nil {
			t.Fatalf("client %s connect: %v", clientName, err)
		}
		t.Cleanup(func() {
			cs.Close()
			_ = cmd.Wait()
		})
		return cs, cmd
	}

	csAlice, _ := makeClient("alice", true, true, nil)
	csBob, _ := makeClient("bob", false, false, &bobAnyHits)

	// Pre-claim from CHATMCP_AGENT_NAME means chat_set_name is unnecessary,
	// but call chat_whoami to confirm identity is bound.
	if r, _ := csAlice.CallTool(ctx, &mcp.CallToolParams{Name: "chat_whoami"}); r.IsError {
		t.Fatalf("whoami: %+v", r)
	}

	if err := csAlice.Subscribe(ctx, &mcp.SubscribeParams{URI: "chatmcp://topic/test"}); err != nil {
		t.Fatalf("alice subscribe topic: %v", err)
	}
	if err := csAlice.Subscribe(ctx, &mcp.SubscribeParams{URI: "chatmcp://inbox/alice"}); err != nil {
		t.Fatalf("alice subscribe inbox: %v", err)
	}
	if err := csBob.Subscribe(ctx, &mcp.SubscribeParams{URI: "chatmcp://topic/test"}); err != nil {
		t.Fatalf("bob subscribe topic: %v", err)
	}

	res, err := csBob.CallTool(ctx, &mcp.CallToolParams{
		Name:      "chat_post",
		Arguments: map[string]any{"topic": "test", "body": "hi @alice from a real subprocess"},
	})
	if err != nil || res.IsError {
		t.Fatalf("bob post: err=%v res=%+v", err, res)
	}

	// Wait up to ~3s for both pushes (poll interval is 50ms).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&aliceTopicHits) >= 1 && atomic.LoadInt64(&aliceInboxHits) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if got := atomic.LoadInt64(&aliceTopicHits); got < 1 {
		t.Errorf("alice should have received a topic update; got %d", got)
	}
	if got := atomic.LoadInt64(&aliceInboxHits); got < 1 {
		t.Errorf("alice should have received an inbox update; got %d", got)
	}

	// Self-echo suppression: bob should not have been notified about his own post.
	time.Sleep(250 * time.Millisecond)
	if got := atomic.LoadInt64(&bobAnyHits); got != 0 {
		t.Errorf("bob (poster) should not receive any push; got %d", got)
	}

	// And read the message back through bob to confirm DB persistence works
	// across the two process boundaries.
	read, err := csBob.CallTool(ctx, &mcp.CallToolParams{
		Name:      "chat_read",
		Arguments: map[string]any{"topic": "test"},
	})
	if err != nil || read.IsError {
		t.Fatalf("bob read: err=%v res=%+v", err, read)
	}
}

// buildBinary compiles cmd/chatmcp into a temp file and returns the path.
// The binary is reused across test runs only within this t.TempDir.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "chatmcp")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stderr = testWriter{t: t, prefix: "go-build"}
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}
	return bin
}

type testWriter struct {
	t      *testing.T
	prefix string
}

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("[%s] %s", w.prefix, string(p))
	return len(p), nil
}
