package server

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/buenemann/chatmcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Server struct {
	mcp *mcp.Server
	db  *store.DB
	pid int

	mu       sync.RWMutex
	identity *store.Agent // nil until claimed via chat_set_name (or pre-claim)
}

func New(db *store.DB) *Server {
	s := &Server{
		db:  db,
		pid: os.Getpid(),
	}

	mcpSrv := mcp.NewServer(
		&mcp.Implementation{Name: "chatmcp", Version: "0.1.0"},
		&mcp.ServerOptions{
			Instructions:       instructions,
			SubscribeHandler:   s.handleSubscribe,
			UnsubscribeHandler: noopUnsubscribe,
			InitializedHandler: s.handleInitialized,
		},
	)
	s.mcp = mcpSrv

	s.registerTools()
	s.registerResources()
	return s
}

func (s *Server) Run(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

func (s *Server) MCP() *mcp.Server { return s.mcp }
func (s *Server) DB() *store.DB    { return s.db }
func (s *Server) PID() int         { return s.pid }

func (s *Server) Identity() *store.Agent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.identity
}

func (s *Server) setIdentity(a *store.Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.identity = a
}

// PreClaim attempts to claim the given name on startup (driven by
// CHATMCP_AGENT_NAME). Returns nil if claim succeeded, or an error describing
// why it didn't (caller logs to stderr and proceeds unclaimed).
func (s *Server) PreClaim(ctx context.Context, name, kind string) error {
	r, err := s.db.ClaimName(ctx, name, kind, s.pid)
	if err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("pre-claim refused: %s %s", r.Reason, r.Message)
	}
	s.setIdentity(r.Agent)
	return nil
}

// requireIdentity returns the bound identity or an error suitable for
// returning from a tool handler when the agent must claim a name first.
func (s *Server) requireIdentity() (*store.Agent, error) {
	id := s.Identity()
	if id == nil {
		return nil, fmt.Errorf("no nickname claimed; call chat_set_name(name) first")
	}
	return id, nil
}

func (s *Server) handleSubscribe(_ context.Context, req *mcp.SubscribeRequest) error {
	parsed, err := ParseURI(req.Params.URI)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	if parsed.Kind == URIKindInbox {
		id := s.Identity()
		if id == nil || id.Name != parsed.Name {
			return fmt.Errorf("subscribe to inbox refused: only the inbox owner may subscribe (claim that nickname via chat_set_name first)")
		}
	}
	return nil
}

func noopUnsubscribe(_ context.Context, _ *mcp.UnsubscribeRequest) error { return nil }

// handleInitialized fires after the client's `notifications/initialized`.
// At this point we know clientInfo.name and can re-register chat_wait with a
// host-specific description that quotes the safe-max timeout for THIS host.
// AddTool with the same name replaces the prior entry and the SDK auto-fires
// notifications/tools/list_changed; well-behaved clients re-fetch and the
// model sees the updated description on its next turn.
func (s *Server) handleInitialized(ctx context.Context, req *mcp.InitializedRequest) {
	kind := ""
	if req != nil && req.Session != nil {
		if p := req.Session.InitializeParams(); p != nil && p.ClientInfo != nil {
			kind = p.ClientInfo.Name
		}
	}
	s.registerChatWaitForHost(kind)
}

// hostSafeMaxTimeoutS returns the chat_wait timeout (in seconds) we'll
// advertise to the agent and clamp to. It's "actual host hard cap minus 1
// second" for hosts that have a tight cap, or a chosen practical ceiling
// for hosts with effectively unlimited caps.
//
// Sources (verified in source code):
//   - Claude Code 2.1.88: services/mcp/client.ts:211 sets default 100_000_000ms
//     (~27.8h); cap chosen practically at 1h.
//   - Codex 0.130.0: codex-mcp/src/rmcp_client.rs:78 sets DEFAULT_TOOL_TIMEOUT
//     to 120s; users can override per-server with tool_timeout_sec.
//   - Gemini CLI: tools/mcp-client.ts:90 sets MCP_DEFAULT_TIMEOUT_MSEC to
//     10*60*1000 (10 min); per-server `timeout` config overrides.
//   - OpenCode: mcp/index.ts:36 sets DEFAULT_TIMEOUT to 30_000ms; calls
//     callTool with resetTimeoutOnProgress: true so progress notifications
//     extend the deadline. We still report the first-interval cap.
func hostSafeMaxTimeoutS(kind string) int {
	switch kind {
	case "claude-code":
		return 3600
	case "codex":
		return 119
	case "gemini-cli":
		return 599
	case "opencode":
		return 29
	default:
		return 25
	}
}

const instructions = `chatmcp lets coding agents (Claude Code, Codex CLI, OpenCode, Gemini CLI) chat in shared topics backed by a local SQLite DB.

To use any chat tool you must first claim a nickname with chat_set_name. On a resumed session, re-claim your previous nickname (it will be transferred to your new process if the prior holder exited).

For sustained multi-agent collaboration, prefer chat_wait over polling chat_read in a tool-call loop: chat_wait blocks server-side until new messages arrive (or its timeout fires), uses zero model tokens during the wait, and returns with everything new. After it returns, process the messages and call it again with since=<last id seen>.`
