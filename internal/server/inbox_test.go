package server_test

import (
	"context"
	"testing"
)

func TestInbox_ListAndDismiss(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	csA, _ := connect(t, ctx, db, "claude-code")
	csB, _ := connect(t, ctx, db, "codex")

	if r := callTool(t, ctx, csA, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("alice claim")
	}
	if r := callTool(t, ctx, csB, "chat_set_name", map[string]any{"name": "bob"}); r.IsError {
		t.Fatal("bob claim")
	}

	// Bob mentions alice twice in two different topics.
	for _, topic := range []string{"general", "design"} {
		if r := callTool(t, ctx, csB, "chat_post", map[string]any{
			"topic": topic, "body": "ping @alice",
		}); r.IsError {
			t.Fatalf("post: %+v", r)
		}
	}

	// Alice reads inbox.
	res := callTool(t, ctx, csA, "chat_inbox", struct{}{})
	var out struct {
		Mentions []struct {
			MessageID int64  `json:"message_id"`
			Topic     string `json:"topic"`
		} `json:"mentions"`
		Dismissed int64 `json:"dismissed"`
	}
	if !structuredOutput(t, res, &out) {
		t.Fatal("no output")
	}
	if len(out.Mentions) != 2 {
		t.Fatalf("expected 2 mentions, got %d", len(out.Mentions))
	}
	if out.Dismissed != 0 {
		t.Errorf("expected no dismiss without flag, got %d", out.Dismissed)
	}

	// Dismiss them.
	res2 := callTool(t, ctx, csA, "chat_inbox", map[string]any{"dismiss": true})
	var out2 struct {
		Dismissed int64 `json:"dismissed"`
	}
	if !structuredOutput(t, res2, &out2) {
		t.Fatal("no output 2")
	}
	if out2.Dismissed != 2 {
		t.Errorf("expected dismissed=2, got %d", out2.Dismissed)
	}

	// Now empty.
	res3 := callTool(t, ctx, csA, "chat_inbox", struct{}{})
	var out3 struct {
		Mentions []struct{} `json:"mentions"`
	}
	if !structuredOutput(t, res3, &out3) {
		t.Fatal("no output 3")
	}
	if len(out3.Mentions) != 0 {
		t.Errorf("expected empty inbox after dismiss, got %d", len(out3.Mentions))
	}
}

func TestInbox_DoesNotIncludeSelfMention(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "claude-code")
	if r := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("claim")
	}
	if r := callTool(t, ctx, cs, "chat_post", map[string]any{
		"topic": "general", "body": "i am @alice and i mention myself",
	}); r.IsError {
		t.Fatal("post")
	}
	res := callTool(t, ctx, cs, "chat_inbox", struct{}{})
	var out struct {
		Mentions []struct{} `json:"mentions"`
	}
	if !structuredOutput(t, res, &out) {
		t.Fatal("no output")
	}
	if len(out.Mentions) != 0 {
		t.Errorf("self-mention should not produce inbox entry; got %d", len(out.Mentions))
	}
}
