package server_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/buenemann/chatmcp/internal/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestResources_ReadTopics(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "claude-code")
	if r := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("claim")
	}
	if r := callTool(t, ctx, cs, "chat_post", map[string]any{"topic": "general", "body": "hi"}); r.IsError {
		t.Fatal("post")
	}

	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: server.URITopics})
	if err != nil {
		t.Fatalf("read topics: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(res.Contents))
	}
	var out struct {
		Topics []struct {
			Name        string `json:"name"`
			MemberCount int    `json:"member_count"`
		} `json:"topics"`
	}
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Topics) != 1 || out.Topics[0].Name != "general" {
		t.Fatalf("unexpected topics: %+v", out)
	}
}

func TestResources_ReadTopicWithPagination(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "claude-code")
	if r := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("claim")
	}
	for i := 0; i < 3; i++ {
		if r := callTool(t, ctx, cs, "chat_post", map[string]any{"topic": "t", "body": "msg"}); r.IsError {
			t.Fatal("post")
		}
	}

	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "chatmcp://topic/t?since=0&limit=2",
	})
	if err != nil {
		t.Fatalf("read topic: %v", err)
	}
	var out struct {
		Topic    string `json:"topic"`
		Messages []struct {
			ID int64 `json:"id"`
		} `json:"messages"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.Topic != "t" || len(out.Messages) != 2 || !out.HasMore {
		t.Fatalf("unexpected: %+v", out)
	}
}

func TestResources_InboxRequiresOwnership(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)

	// alice and bob exist; subscriber is alice but tries to read bob's inbox.
	if _, err := db.ClaimName(ctx, "bob", "codex", 1); err != nil {
		t.Fatal(err)
	}

	cs, _ := connect(t, ctx, db, "claude-code")
	if r := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("claim")
	}

	_, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "chatmcp://inbox/bob"})
	if err == nil {
		t.Fatal("expected ReadResource on someone else's inbox to fail")
	}
}

func TestResources_SubscribeInboxRefusedForOthers(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	if _, err := db.ClaimName(ctx, "bob", "codex", 1); err != nil {
		t.Fatal(err)
	}
	cs, _ := connect(t, ctx, db, "claude-code")
	if r := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("claim")
	}

	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: "chatmcp://inbox/bob"}); err == nil {
		t.Fatal("expected subscribe to bob's inbox to be refused")
	}
}

func TestResources_SubscribeInboxOwnerAllowed(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "claude-code")
	if r := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("claim")
	}
	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: "chatmcp://inbox/alice"}); err != nil {
		t.Fatalf("expected own-inbox subscribe to succeed: %v", err)
	}
}
