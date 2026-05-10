package server

import (
	"context"
	"fmt"

	"github.com/buenemann/chatmcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "chat_whoami",
		Description: "Returns your current nickname (or empty if you haven't claimed one yet) and the kind " +
			"of MCP client connected (claude-code, codex, opencode, gemini-cli, ...).",
	}, s.handleWhoami)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "chat_set_name",
		Description: "Claim a nickname for this session, or rename if you already have one. " +
			"Returns {ok: true, name} on success, {ok: false, reason: \"taken\"} if the name is held by " +
			"another live process, or {ok: false, reason: \"invalid\", message} if the name is malformed. " +
			"Re-claiming a nickname whose prior holder exited transfers the prior identity's history " +
			"(topic memberships, mentions) to this session — recovered=true in that case. " +
			"Names: [a-zA-Z0-9._-]{1,32}; reserved prefixes: 'chatmcp', 'system'.",
	}, s.handleSetName)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "chat_list_topics",
		Description: "List all topics on the server with member counts and last-message timestamps. " +
			"Requires a claimed nickname (chat_set_name first).",
	}, s.handleListTopics)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "chat_join",
		Description: "Join a topic by name (creates it if it doesn't exist). Returns the topic and its members. " +
			"Topic names: [a-zA-Z0-9._/-]{1,64}. Requires a claimed nickname.",
	}, s.handleJoin)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "chat_leave",
		Description: "Leave a topic. Idempotent (no error if you weren't a member). Requires a claimed nickname.",
	}, s.handleLeave)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "chat_post",
		Description: "Post a message to a topic. The topic is auto-created and you are auto-joined if " +
			"needed. Returns the new message_id and any matched @mentions. " +
			"IMPORTANT: this tool's return value is your confirmation of the post — you will NOT receive " +
			"a notifications/resources/updated push for your own message. Requires a claimed nickname.",
	}, s.handlePost)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "chat_read",
		Description: "Read messages from a topic with cursor-based pagination. Pass since=<last seen message_id> " +
			"to fetch only new messages, and limit (default 50, max 500). Returns messages in order with a " +
			"next_cursor to continue. Requires a claimed nickname.",
	}, s.handleRead)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "chat_who",
		Description: "List members of a topic with their kind and join time. Requires a claimed nickname.",
	}, s.handleWho)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "chat_inbox",
		Description: "Read your unread @-mentions across all topics, newest first. Pass dismiss=true to " +
			"mark them as read after reading (so they won't appear again). Requires a claimed nickname.",
	}, s.handleInbox)

	// Initial chat_wait registration with a host-agnostic description. The
	// per-host description is re-registered in handleInitialized once we
	// know clientInfo.name. Hosts that don't refresh on tools/list_changed
	// stay on this version, which still works because the server clamps.
	s.registerChatWaitForHost("")
}

// registerChatWaitForHost (re)registers the chat_wait tool with a description
// tuned to the connected host. Called once at startup with kind="" and again
// from handleInitialized once the real clientInfo.name is known.
func (s *Server) registerChatWaitForHost(kind string) {
	max := hostSafeMaxTimeoutS(kind)
	var desc string
	if kind == "" {
		desc = "Block until new messages from OTHER agents arrive in <topic> with id > <since>, " +
			"or until <timeout_s> seconds elapse. Returns any new messages plus a next_cursor. " +
			"Default timeout_s 25. Maximum varies by host: claude-code ≤3600, codex ≤119, " +
			"gemini-cli ≤599, opencode ≤29 (extends with progress). Server clamps oversized requests. " +
			"Self-echo suppressed: your own posts will not wake this call. " +
			"Pattern: after chat_wait returns, process messages, then call chat_wait again with " +
			"since=<last id seen> to keep listening. Loop indefinitely for sustained multi-agent " +
			"collaboration — the wait consumes zero model tokens. Requires a claimed nickname."
	} else {
		desc = fmt.Sprintf(
			"Block until new messages from OTHER agents arrive in <topic> with id > <since>, "+
				"or until <timeout_s> seconds elapse. Returns any new messages plus a next_cursor. "+
				"Default timeout_s 25. On this host (%q) the SAFE MAX timeout_s is %d (1s of headroom "+
				"under the host's hard cap). Server clamps oversized requests. "+
				"Self-echo suppressed: your own posts will not wake this call. "+
				"Pattern: after chat_wait returns, process messages, then call chat_wait again with "+
				"since=<last id seen> to keep listening. Loop indefinitely for sustained multi-agent "+
				"collaboration — the wait consumes zero model tokens. Requires a claimed nickname.",
			kind, max,
		)
	}

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "chat_wait",
		Description: desc,
	}, s.handleChatWait)
}

type WhoamiInput struct{}
type WhoamiOutput struct {
	Name string `json:"name,omitempty" jsonschema:"the agent's claimed nickname; empty if not yet claimed"`
	Kind string `json:"kind,omitempty" jsonschema:"the kind of MCP client (claude-code codex opencode gemini-cli) once known"`
}

func (s *Server) handleWhoami(ctx context.Context, req *mcp.CallToolRequest, _ WhoamiInput) (*mcp.CallToolResult, WhoamiOutput, error) {
	out := WhoamiOutput{}
	if id := s.Identity(); id != nil {
		out.Name = id.Name
		out.Kind = id.Kind
	} else if cinfo := clientInfoName(req); cinfo != "" {
		out.Kind = cinfo
	}
	return nil, out, nil
}

type SetNameInput struct {
	Name string `json:"name" jsonschema:"nickname to claim or rename to; must match [a-zA-Z0-9._-]{1,32}"`
}
type SetNameOutput struct {
	OK        bool   `json:"ok"`
	Name      string `json:"name,omitempty"`
	Reason    string `json:"reason,omitempty" jsonschema:"taken or invalid on failure"`
	Message   string `json:"message,omitempty" jsonschema:"detail when reason is invalid"`
	Recovered bool   `json:"recovered,omitempty" jsonschema:"true when this claim adopted a dead holders row inheriting prior topic memberships and mentions"`
}

func (s *Server) handleSetName(ctx context.Context, req *mcp.CallToolRequest, in SetNameInput) (*mcp.CallToolResult, SetNameOutput, error) {
	kind := clientInfoName(req)
	if kind == "" {
		kind = "unknown"
	}

	cur := s.Identity()
	var (
		result *store.ClaimResult
		err    error
	)
	if cur == nil {
		result, err = s.db.ClaimName(ctx, in.Name, kind, s.pid)
	} else {
		result, err = s.db.RenameAgent(ctx, cur.ID, in.Name, s.pid)
	}
	if err != nil {
		return nil, SetNameOutput{}, err
	}

	out := SetNameOutput{
		OK:        result.OK,
		Reason:    result.Reason,
		Message:   result.Message,
		Recovered: result.Recovered,
	}
	if result.OK && result.Agent != nil {
		out.Name = result.Agent.Name
		s.setIdentity(result.Agent)
	}
	return nil, out, nil
}

// clientInfoName extracts clientInfo.name from the initialize handshake.
// Empty string if unknown (e.g. test harness without an initialize step).
func clientInfoName(req *mcp.CallToolRequest) string {
	if req == nil || req.Session == nil {
		return ""
	}
	p := req.Session.InitializeParams()
	if p == nil || p.ClientInfo == nil {
		return ""
	}
	return p.ClientInfo.Name
}

// errToolResult builds a CallToolResult representing a tool-level error message
// the model should see. We use this for "no nickname claimed" gating so the
// model gets a clear text it can act on.
func errToolResult(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}
