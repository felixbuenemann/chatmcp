package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/buenemann/chatmcp/internal/store"
)

func newTestDB(t *testing.T) *store.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestClaimName_Fresh(t *testing.T) {
	db := newTestDB(t)
	r, err := db.ClaimName(context.Background(), "alice", "claude-code", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK || r.Agent == nil || r.Agent.Name != "alice" || r.Recovered {
		t.Fatalf("unexpected: %+v", r)
	}
}

func TestClaimName_TakenByLiveOther(t *testing.T) {
	db := newTestDB(t)
	// First claimant uses our PID, locking it.
	if r, _ := db.ClaimName(context.Background(), "alice", "claude-code", os.Getpid()); !r.OK {
		t.Fatalf("first claim must succeed: %+v", r)
	}
	// Second claimant (different "PID") should be told "taken" via the live check —
	// but since pidCur == ourPID we'd hit the idempotent branch. Simulate a different
	// live PID by passing pid=1 (init, always alive).
	r, err := db.ClaimName(context.Background(), "alice", "claude-code", 999999)
	if err != nil {
		t.Fatal(err)
	}
	if r.OK {
		t.Fatalf("expected taken, got OK: %+v", r)
	}
	if r.Reason != "taken" {
		t.Fatalf("expected reason=taken, got %q", r.Reason)
	}
}

func TestClaimName_Recovers_DeadHolder(t *testing.T) {
	db := newTestDB(t)

	// Seed a row whose PID is definitely dead. Negative or absurdly high PIDs
	// fail kill(pid, 0) with ESRCH on Darwin/Linux.
	deadPID := 2
	for store.IsPIDAlive(deadPID) {
		deadPID++
		if deadPID > 1<<22 {
			t.Skip("could not find a dead PID on this system")
		}
	}

	if r, _ := db.ClaimName(context.Background(), "alice", "claude-code", deadPID); !r.OK {
		t.Fatalf("seed claim: %+v", r)
	}
	// Now claim from a live process.
	r, err := db.ClaimName(context.Background(), "alice", "claude-code", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK || !r.Recovered || r.Agent.PID != os.Getpid() {
		t.Fatalf("expected recovered claim, got %+v", r)
	}
}

func TestClaimName_Idempotent(t *testing.T) {
	db := newTestDB(t)
	pid := os.Getpid()
	r1, _ := db.ClaimName(context.Background(), "alice", "claude-code", pid)
	r2, _ := db.ClaimName(context.Background(), "alice", "claude-code", pid)
	if !r1.OK || !r2.OK {
		t.Fatal("both should succeed")
	}
	if r1.Agent.ID != r2.Agent.ID {
		t.Fatalf("expected same agent_id; got %d vs %d", r1.Agent.ID, r2.Agent.ID)
	}
	if r2.Recovered {
		t.Fatalf("idempotent re-claim should not be flagged as recovered")
	}
}

func TestRename_FreeName(t *testing.T) {
	db := newTestDB(t)
	pid := os.Getpid()
	r, _ := db.ClaimName(context.Background(), "alice", "claude-code", pid)
	if !r.OK {
		t.Fatal("setup")
	}
	r2, err := db.RenameAgent(context.Background(), r.Agent.ID, "alice-prime", pid)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.OK || r2.Agent.Name != "alice-prime" || r2.Agent.ID != r.Agent.ID {
		t.Fatalf("unexpected: %+v", r2)
	}
}

func TestRename_TakenName(t *testing.T) {
	db := newTestDB(t)
	pid := os.Getpid()
	r1, _ := db.ClaimName(context.Background(), "alice", "claude-code", pid)
	r2, _ := db.ClaimName(context.Background(), "bob", "codex", 999999) // pretend live
	if !r1.OK || !r2.OK {
		t.Fatal("setup")
	}
	r3, _ := db.RenameAgent(context.Background(), r1.Agent.ID, "bob", pid)
	if r3.OK || r3.Reason != "taken" {
		t.Fatalf("expected rename refused as taken, got %+v", r3)
	}
}

func TestValidateName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"alice", true},
		{"alice.bob", true},
		{"a-b_c.1", true},
		{"", false},
		{"name with spaces", false},
		{"chatmcp", false},
		{"system-bot", false},
		{"WAY-TOO-LONG-NAME-THAT-EXCEEDS-THIRTY-TWO-CHARACTERS", false},
	}
	for _, c := range cases {
		_, ok := store.ValidateName(c.name)
		if ok != c.ok {
			t.Errorf("ValidateName(%q) ok=%v, want %v", c.name, ok, c.ok)
		}
	}
}
