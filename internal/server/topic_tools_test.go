package server_test

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTools_RequireIdentity(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "claude-code")

	cases := []struct {
		name string
		args any
	}{
		{"chat_list_topics", struct{}{}},
		{"chat_join", map[string]any{"topic": "test"}},
		{"chat_post", map[string]any{"topic": "test", "body": "hi"}},
		{"chat_read", map[string]any{"topic": "test"}},
		{"chat_who", map[string]any{"topic": "test"}},
	}
	for _, c := range cases {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: c.name, Arguments: c.args})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !res.IsError {
			t.Errorf("%s: expected IsError=true before claim, got %+v", c.name, res)
		}
	}
}

func TestTools_PostJoinReadFlow(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	csA, _ := connect(t, ctx, db, "claude-code")
	csB, _ := connect(t, ctx, db, "codex")

	// Set up identities. B uses a different live PID via the seeded path.
	if r := callTool(t, ctx, csA, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatalf("alice claim: %+v", r)
	}
	// B claims a separate name in the same process — both see os.Getpid(),
	// which means alice's claim looks like ourPid for B too. To make B a
	// distinct identity that posts under "bob", we need to seed bob via
	// the store path with a live foreign PID, then set up B's session
	// after — but since the in-memory transport is process-local, "bob"
	// from inside csB's tool calls will go through ClaimName from a
	// process whose pid == ours, not 1. ClaimName: name="bob" doesn't
	// exist → fresh insert with our PID. Fine.
	if r := callTool(t, ctx, csB, "chat_set_name", map[string]any{"name": "bob"}); r.IsError {
		t.Fatalf("bob claim: %+v", r)
	}

	// Alice posts a mention.
	post := callTool(t, ctx, csA, "chat_post", map[string]any{"topic": "general", "body": "hello @bob"})
	if post.IsError {
		t.Fatalf("post: %+v", post)
	}
	var postOut struct {
		MessageID int64    `json:"message_id"`
		Mentions  []string `json:"mentions"`
	}
	if !structuredOutput(t, post, &postOut) {
		t.Fatal("no structured output")
	}
	if postOut.MessageID == 0 {
		t.Errorf("expected nonzero message_id")
	}
	if len(postOut.Mentions) != 1 || postOut.Mentions[0] != "bob" {
		t.Errorf("expected mentions=[bob], got %v", postOut.Mentions)
	}

	// Bob joins and reads.
	if r := callTool(t, ctx, csB, "chat_join", map[string]any{"topic": "general"}); r.IsError {
		t.Fatalf("join: %+v", r)
	}
	read := callTool(t, ctx, csB, "chat_read", map[string]any{"topic": "general"})
	if read.IsError {
		t.Fatalf("read: %+v", read)
	}
	var readOut struct {
		Messages []struct {
			ID         int64  `json:"id"`
			AuthorName string `json:"author_name"`
			Body       string `json:"body"`
		} `json:"messages"`
	}
	if !structuredOutput(t, read, &readOut) {
		t.Fatal("no structured output from read")
	}
	if len(readOut.Messages) != 1 || readOut.Messages[0].Body != "hello @bob" || readOut.Messages[0].AuthorName != "alice" {
		t.Fatalf("unexpected messages: %+v", readOut.Messages)
	}

	// who: bob and alice should both be members (alice via auto-join on post).
	who := callTool(t, ctx, csA, "chat_who", map[string]any{"topic": "general"})
	var whoOut struct {
		Members []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"members"`
	}
	if !structuredOutput(t, who, &whoOut) {
		t.Fatal("no who output")
	}
	if len(whoOut.Members) != 2 {
		t.Fatalf("expected 2 members, got %d (%+v)", len(whoOut.Members), whoOut.Members)
	}
}

func TestTools_ReadPagination(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	csA, _ := connect(t, ctx, db, "claude-code")
	if r := callTool(t, ctx, csA, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("claim")
	}

	for i := 0; i < 5; i++ {
		if r := callTool(t, ctx, csA, "chat_post", map[string]any{"topic": "t", "body": "msg"}); r.IsError {
			t.Fatal("post")
		}
	}

	// Read with limit 2: should get 2 + has_more.
	res := callTool(t, ctx, csA, "chat_read", map[string]any{"topic": "t", "limit": 2})
	var out struct {
		Messages   []struct{ ID int64 } `json:"messages"`
		NextCursor int64                `json:"next_cursor"`
		HasMore    bool                 `json:"has_more"`
	}
	if !structuredOutput(t, res, &out) {
		t.Fatal("no output")
	}
	if len(out.Messages) != 2 || !out.HasMore {
		t.Fatalf("expected 2 msgs + has_more, got %+v", out)
	}

	// Continue with cursor.
	res2 := callTool(t, ctx, csA, "chat_read", map[string]any{"topic": "t", "since": out.NextCursor, "limit": 2})
	var out2 struct {
		Messages []struct{ ID int64 } `json:"messages"`
	}
	if !structuredOutput(t, res2, &out2) || len(out2.Messages) != 2 {
		t.Fatalf("expected 2 next msgs, got %+v", out2)
	}
}
