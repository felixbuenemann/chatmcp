package server_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/buenemann/chatmcp/internal/server"
	"github.com/buenemann/chatmcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect spins up a chatmcp server bound to the given store and a fresh in-memory
// MCP client/server pair. The returned client session can call tools immediately.
func connect(t *testing.T, ctx context.Context, db *store.DB, clientName string) (*mcp.ClientSession, *server.Server) {
	t.Helper()
	srv := server.New(db)

	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.MCP().Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	if clientName == "" {
		clientName = "test-client"
	}
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "test"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, srv
}

func openTempDB(t *testing.T) *store.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func callTool(t *testing.T, ctx context.Context, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

// structuredOutput pulls the StructuredContent out of a CallToolResult into a typed
// destination. Returns false if no structured output present.
func structuredOutput(t *testing.T, res *mcp.CallToolResult, dst any) bool {
	t.Helper()
	if res.StructuredContent == nil {
		return false
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured: %v", err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("unmarshal structured: %v", err)
	}
	return true
}

func TestIdentity_WhoamiBeforeClaim(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "claude-code")

	res := callTool(t, ctx, cs, "chat_whoami", struct{}{})
	if res.IsError {
		t.Fatalf("chat_whoami should not error before claim: %+v", res)
	}
	var out struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if !structuredOutput(t, res, &out) {
		t.Fatal("expected structured output from chat_whoami")
	}
	if out.Name != "" {
		t.Errorf("expected empty name pre-claim, got %q", out.Name)
	}
	if out.Kind != "claude-code" {
		t.Errorf("expected kind=claude-code from clientInfo, got %q", out.Kind)
	}
}

func TestIdentity_SetNameClaimsAndUpdatesWhoami(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "claude-code")

	res := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"})
	var out struct {
		OK   bool   `json:"ok"`
		Name string `json:"name"`
	}
	if !structuredOutput(t, res, &out) || !out.OK || out.Name != "alice" {
		t.Fatalf("set_name failed: %+v", res)
	}

	res2 := callTool(t, ctx, cs, "chat_whoami", struct{}{})
	var out2 struct {
		Name string `json:"name"`
	}
	if !structuredOutput(t, res2, &out2) || out2.Name != "alice" {
		t.Fatalf("whoami after claim: got %+v", res2)
	}
}

func TestIdentity_SetNameInvalid(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "claude-code")

	res := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "bad name with spaces"})
	var out struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	if !structuredOutput(t, res, &out) {
		t.Fatal("no structured output")
	}
	if out.OK || out.Reason != "invalid" {
		t.Fatalf("expected invalid, got %+v", out)
	}
}

func TestIdentity_SetNameTakenByOtherProcess(t *testing.T) {
	// Two separate Server instances sharing one DB; the second's claim must be
	// refused since the first holder is alive (its PID == ours, but it's a
	// different in-process Server with the same os.Getpid). We simulate
	// "different live PID" by directly writing into the agents table.
	ctx := context.Background()
	db := openTempDB(t)

	// Pre-seed the agents table with another live PID (use 1, init, always alive).
	if _, err := db.ClaimName(ctx, "alice", "codex", 1); err != nil {
		t.Fatal(err)
	}

	cs, _ := connect(t, ctx, db, "claude-code")
	res := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"})
	var out struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	if !structuredOutput(t, res, &out) {
		t.Fatal("no structured output")
	}
	if out.OK || out.Reason != "taken" {
		t.Fatalf("expected taken, got %+v", out)
	}
}

func TestIdentity_SetNameRecoversDeadHolder(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)

	// Find a dead PID and seed the row.
	deadPID := 2
	for store.IsPIDAlive(deadPID) {
		deadPID++
		if deadPID > 1<<22 {
			t.Skip("could not find a dead PID")
		}
	}
	if _, err := db.ClaimName(ctx, "alice", "codex", deadPID); err != nil {
		t.Fatal(err)
	}

	cs, _ := connect(t, ctx, db, "claude-code")
	res := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"})
	var out struct {
		OK        bool `json:"ok"`
		Recovered bool `json:"recovered"`
	}
	if !structuredOutput(t, res, &out) {
		t.Fatal("no structured output")
	}
	if !out.OK || !out.Recovered {
		t.Fatalf("expected recovered claim, got %+v", out)
	}
}

func TestIdentity_Rename(t *testing.T) {
	ctx := context.Background()
	db := openTempDB(t)
	cs, _ := connect(t, ctx, db, "claude-code")

	if r := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice"}); r.IsError {
		t.Fatal("first claim")
	}
	res := callTool(t, ctx, cs, "chat_set_name", map[string]any{"name": "alice-prime"})
	var out struct {
		OK   bool   `json:"ok"`
		Name string `json:"name"`
	}
	if !structuredOutput(t, res, &out) {
		t.Fatal("no structured output")
	}
	if !out.OK || out.Name != "alice-prime" {
		t.Fatalf("rename failed: %+v", out)
	}
}
