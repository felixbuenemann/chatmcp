# Docker E2E: two real coding agents talking via chatmcp

End-to-end tests with **real coding-agent SDKs** rather than synthetic MCP clients. Two containers each run a coding-agent SDK with `chatmcp` wired in as a stdio MCP server, and share one SQLite WAL file via a named Docker volume.

Four scenarios:

| Scenario | What it proves | Run script |
|---|---|---|
| Same-vendor PINGBACK (Claude × Claude) | The basic mechanics: alice can mention @bob, bob can reply, both see each other's messages through chatmcp + shared volume. | `./run.sh` |
| Cross-vendor PINGBACK (Claude × Codex) | Two coding agents from *different vendors* can collaborate via chatmcp without protocol-level surprises. | `./run-cross.sh` |
| Multi-turn multiply (Claude × Claude or Claude × Codex) | True bidirectional reasoning: each agent has private state. Alice asks bob for his secret number, bob replies, alice multiplies by her own secret and posts the product. The verifier checks `product = alice_secret × bob_secret`. Random secrets per run, so the agents must actually communicate the value rather than echo a constant. | `./run-multiply.sh` (add `CROSS=1` for cross-vendor) |
| **Autonomous chat (chat_wait)** | The agents' prompts FORBID polling via chat_read/chat_inbox. They must use `chat_wait` (long-poll, server-side blocking) as their only mechanism to receive messages. Proves chat_wait enables sustained real-time multi-agent collaboration without per-turn polling instructions. Each agent uses ~4 tool calls vs 5-7+ in the polling version. | `./run-autochat.sh` (add `CROSS=1` for cross-vendor) |

## What it proves

- A real LLM-driven agent can pick up the `chat_*` tool descriptions from the MCP `tools/list` and use them correctly without bespoke prompting beyond the role.
- Two independent OS processes — backed by independent `chatmcp` subprocesses inside independent containers — can coordinate via the shared SQLite database.
- `chat_post` / `chat_inbox` / `chat_read` round-trip through Docker's volume layer.
- Mention parsing actually delivers cross-container.

## What it doesn't prove

- It does **not** exercise the resource-subscription push path on the Agent SDK side — the SDK's `query()` does not surface `notifications/resources/updated` to the model. Both agents poll via `chat_read` / `chat_inbox`. That said, `chatmcp`'s notifier still runs and emits notifications that the SDK silently discards.
- The agent prompts are explicit step-by-step instructions. A more open-ended prompt ("just chat with bob about the codebase") would test natural-language fluency too — out of scope here.

## Cost & opt-in

These tests call real APIs. There is no mock — that would defeat the purpose. Set the appropriate API key(s) to opt in.

Measured costs from passing runs (default models):

**Same-vendor** (`./run.sh`, both on `claude-haiku-4-5`):

| Agent | Wall time | Tool calls | Cost |
|---|---|---|---|
| bob   | 11.7s | 6 | $0.0087 |
| alice | 11.7s | 5 | $0.0089 |
| **total** | | | **~$0.018** |

**Cross-vendor** (`./run-cross.sh`, alice on `claude-haiku-4-5`, bob on `gpt-5-mini` + `reasoning=low`):

| Agent | SDK | Wall time | Tool calls | Cost |
|---|---|---|---|---|
| bob   | Codex (gpt-5-mini)            | 8.4s  | 5 | ~$0.003 (87k in, 57k cached, 106 out) |
| alice | Claude Agent SDK (haiku-4-5)  | 14.5s | 5 | $0.0086 |
| **total** | | | | **~$0.012** |

**Multi-turn multiply, same-vendor** (`./run-multiply.sh`, both on `claude-haiku-4-5`, secrets 9 × 2 = 18):

| Agent | SDK | Wall time | Tool calls | Cost |
|---|---|---|---|---|
| alice | Claude Agent SDK (haiku-4-5) | 15.0s | 5 | $0.0147 |
| bob   | Claude Agent SDK (haiku-4-5) | 15.7s | 6 | $0.0169 |
| **total** | | | | **~$0.032** |

**Multi-turn multiply, cross-vendor** (`CROSS=1 ./run-multiply.sh`, secrets 6 × 6 = 36):

| Agent | SDK | Wall time | Tool calls | Cost |
|---|---|---|---|---|
| alice | Claude Agent SDK (haiku-4-5) | 20.8s | 11 | $0.0232 |
| bob   | Codex (gpt-5-mini)           | 27.0s | 5  | ~$0.005 |
| **total** | | | | **~$0.028** |

**Autonomous chat (`./run-autochat.sh`)** — chat_wait only, no polling instructions:

| Agent | SDK | Wall time | Tool calls | Cost |
|---|---|---|---|---|
| alice | Claude Agent SDK (haiku-4-5) | 12.3s | 4 | $0.0140 |
| bob   | Claude Agent SDK (haiku-4-5) | 12.5s | 4 | $0.0089 |
| **same-vendor total** | | | | **~$0.023** |

| Agent | SDK | Wall time | Tool calls | Cost |
|---|---|---|---|---|
| alice | Claude Agent SDK (haiku-4-5) | 14.2s | 4 | $0.0085 |
| bob   | Codex (gpt-5-mini)           | 13.1s | 4 | ~$0.005 |
| **cross-vendor total** | | | | **~$0.014** |

The autochat numbers are tighter because each agent makes a single `chat_wait` call that blocks server-side until the peer posts — no polling iterations, no model tokens spent waiting.

Numbers vary ±20% run-to-run depending on each model's reasoning overhead.

## Run

Same-vendor (Claude × Claude):

```bash
export ANTHROPIC_API_KEY=sk-ant-...
./test/e2e/docker/run.sh
```

Cross-vendor (Claude × Codex):

```bash
export ANTHROPIC_API_KEY=sk-ant-...
export OPENAI_API_KEY=sk-...
./test/e2e/docker/run-cross.sh
```

Multi-turn multiply (random secrets, verified product):

```bash
export ANTHROPIC_API_KEY=sk-ant-...
./test/e2e/docker/run-multiply.sh                  # same-vendor
CROSS=1 OPENAI_API_KEY=sk-... ./test/e2e/docker/run-multiply.sh   # cross-vendor

# Pin secrets for reproducibility:
ALICE_SECRET=7 BOB_SECRET=11 ./test/e2e/docker/run-multiply.sh    # expects product=77
```

Autonomous chat (chat_wait long-poll, no polling instructions):

```bash
export ANTHROPIC_API_KEY=sk-ant-...
./test/e2e/docker/run-autochat.sh                  # same-vendor
CROSS=1 OPENAI_API_KEY=sk-... ./test/e2e/docker/run-autochat.sh   # cross-vendor
```

The script:

1. Builds the `chatmcp-e2e-agent` image (Go build of `chatmcp` + Node base + Agent SDK).
2. Brings up two containers (`alice`, `bob`) sharing the named volume `chatmcp-e2e-chat-data`.
3. Lets both agents run to natural completion (300s wall-clock cap as a safety net). Bob exits when he sees alice's mention and replies with PINGBACK; alice exits when she sees bob's PINGBACK reply.
4. Reads the shared SQLite DB via a one-shot container and asserts:
   - At least 1 message authored by `alice` containing `@bob`.
   - At least 1 message authored by `bob` containing `PINGBACK`.
   - At least 1 row in the `mentions` table.
5. Tears down compose and removes the volume.

A successful run prints `✓ E2E PASSED` and exits 0.

## Override the model

```bash
# Same-vendor
CHATMCP_E2E_MODEL=claude-sonnet-4-6 ./run.sh

# Cross-vendor — separate vars per side
CLAUDE_E2E_MODEL=claude-sonnet-4-6 CODEX_E2E_MODEL=gpt-5 ./run-cross.sh
```

Sonnet is more reliable on the polling task but ~5× more expensive than Haiku for this exchange. Opus is overkill for this; Haiku 4.5 is the right Claude default. For Codex, `gpt-5-mini` with `modelReasoningEffort: "low"` works well; `gpt-5` is overkill.

## Inspect a stuck run

If a container hangs and you want to see the SDK's message stream while it's running, pop another shell:

```bash
docker compose logs -f alice    # or bob
```

After teardown, the named volume is gone, but you can preserve it for forensic inspection by skipping the cleanup step:

```bash
DEBUG=1 ./run.sh   # (not yet implemented; rerun without `down -v` manually)
```

## Files

| File | Purpose |
|---|---|
| `Dockerfile` | Multi-stage: builds `chatmcp` (Go 1.26-alpine), then ships it in a Node 22 slim image with both Agent SDKs and the `node` user (the Claude/Codex CLIs both refuse to run as root). |
| `docker-compose.yml` | Two services (`alice`, `bob`) sharing the `chatmcp-e2e-chat-data` named volume. Default config: both use `CHATMCP_E2E_SDK=claude`. |
| `docker-compose.cross.yml` | Override for `run-cross.sh`. Switches bob to `CHATMCP_E2E_SDK=codex` and forwards `OPENAI_API_KEY`. |
| `docker-compose.multiply.yml` | Override for `run-multiply.sh`. Sets `CHATMCP_E2E_TASK=multiply` and per-agent `MY_SECRET` env vars. |
| `docker-compose.autochat.yml` | Override for `run-autochat.sh`. Sets `CHATMCP_E2E_TASK=autochat` so agents use only `chat_wait`. |
| `agent.mjs` | The Agent SDK driver — single file, dispatches on `CHATMCP_E2E_SDK` (claude/codex) and `CHATMCP_E2E_TASK` (ping/multiply). Picks role from argv, runs the role's prompt against `chatmcp`, exits 0 on `DONE`, 3 on `TIMEOUT` or anything else. |
| `package.json` | Pins both `@anthropic-ai/claude-agent-sdk` and `@openai/codex-sdk`. |
| `run.sh` | Same-vendor PINGBACK orchestration. |
| `run-cross.sh` | Cross-vendor PINGBACK orchestration. |
| `run-multiply.sh` | Multi-turn multiply orchestration. Generates random secrets, runs the agents, asserts the posted product equals `alice_secret × bob_secret`. `CROSS=1` switches bob to Codex. |
| `run-autochat.sh` | Autonomous-chat orchestration. Agents may only use chat_wait to receive messages; verifies a successful PINGBACK round-trip with no polling. `CROSS=1` switches bob to Codex. |

## Gotchas captured here for future debugging

- **Go toolchain pin.** `go.mod` requires Go 1.26+; the build stage uses `golang:1.26-alpine`. Bumping `go.mod` requires bumping the Dockerfile in lockstep.
- **Both CLIs refuse root.** Claude's `--dangerously-skip-permissions` and Codex's permission system both error out as uid 0. Reuse the `node` user (uid 1000) baked into `node:22-slim`.
- **Codex MCP auto-approval requires `sandboxMode: "danger-full-access"`.** With `read-only` or `workspace-write`, every MCP tool call falls through to "ask the user" and auto-cancels in headless mode (`error: "user cancelled MCP tool call"`). Inside an ephemeral container this is fine.
- **`@openai/codex-sdk` ships a musl Linux binary** (`@openai/codex-linux-arm64/.../aarch64-unknown-linux-musl/codex`). It runs cleanly on `node:22-slim` (Debian/glibc).
- **Codex SDK's `config: { mcp_servers.<name>.env: { ... } }` flattening is inconsistent across runs.** Sometimes bob's `CHATMCP_AGENT_NAME` env reaches the chatmcp subprocess via the SDK's `--config` flattening, sometimes it doesn't. When it doesn't, bob's first chat tool call hits the identity gate and bob self-corrects by calling `chat_set_name("bob")`. Test still passes either way; cost difference is one extra tool call when the pre-claim is missed.
- **`gpt-5-nano` does NOT work for this task.** Tried at both `modelReasoningEffort: "low"` and `"medium"`. In both cases the model invented a non-existent `read_mcp_resource` tool (with bogus URIs like `chat_set_name:bob` or `/`) instead of calling the real `mcp__chat__*` tools, then gave up after one failed call. `gpt-5-mini` at `"low"` reasoning is the cheapest model that reliably completes the exchange.
