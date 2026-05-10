package server_test

import (
	"context"
	"testing"

	"github.com/buenemann/chatmcp/internal/store"
)

// TestReclaim_InheritsTopicMemberships verifies that when a session re-claims
// a name held by a now-dead PID, the new session inherits the prior identity's
// topic memberships and unread mentions — the "claude --resume" scenario.
func TestReclaim_InheritsTopicMemberships(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)

	// Find a dead PID to seed with.
	deadPID := 2
	for store.IsPIDAlive(deadPID) {
		deadPID++
		if deadPID > 1<<22 {
			t.Skip("no dead PID")
		}
	}

	// Seed: alice claims her name with a dead PID, joins #project, gets mentioned by bob.
	r, _ := db.ClaimName(ctx, "alice", "claude-code", deadPID)
	if !r.OK {
		t.Fatal("seed alice")
	}
	aliceID := r.Agent.ID
	if _, _, err := db.JoinTopic(ctx, "project", aliceID); err != nil {
		t.Fatal(err)
	}

	rb, _ := db.ClaimName(ctx, "bob", "codex", 1) // bob with live PID
	if !rb.OK {
		t.Fatal("seed bob")
	}
	if _, err := db.PostMessage(ctx, "project", rb.Agent, "ping @alice"); err != nil {
		t.Fatal(err)
	}

	// Now a fresh session arrives and re-claims alice via chat_set_name.
	cs, _ := connect(t, ctx, db, "claude-code")
	res := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"})
	var out struct {
		OK        bool `json:"ok"`
		Recovered bool `json:"recovered"`
	}
	if !structuredOutput(t, res, &out) || !out.OK || !out.Recovered {
		t.Fatalf("expected recovered claim, got %+v", out)
	}

	// History inheritance: the new session should see alice as a member of #project,
	// and her inbox should have bob's mention.
	whoRes := callTool(t, ctx, cs, "chat_who", map[string]any{"topic": "project"})
	var whoOut struct {
		Members []struct {
			Name string `json:"name"`
		} `json:"members"`
	}
	if !structuredOutput(t, whoRes, &whoOut) {
		t.Fatal("no who output")
	}
	hasAlice := false
	for _, m := range whoOut.Members {
		if m.Name == "alice" {
			hasAlice = true
		}
	}
	if !hasAlice {
		t.Errorf("recovered alice should still be a member of #project; got %+v", whoOut.Members)
	}

	inbox := callTool(t, ctx, cs, "chat_inbox", struct{}{})
	var inboxOut struct {
		Mentions []struct {
			AuthorName string `json:"author_name"`
			Body       string `json:"body"`
		} `json:"mentions"`
	}
	if !structuredOutput(t, inbox, &inboxOut) {
		t.Fatal("no inbox output")
	}
	if len(inboxOut.Mentions) != 1 || inboxOut.Mentions[0].AuthorName != "bob" {
		t.Fatalf("expected one inherited mention from bob; got %+v", inboxOut.Mentions)
	}
}
