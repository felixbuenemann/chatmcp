// Driver for the chatmcp Docker E2E test. One file, two SDKs, two scenarios.
//
// argv[2]                   role: "alice" | "bob"
// env CHATMCP_E2E_SDK       which agent SDK to drive: "claude" (default) | "codex"
// env CHATMCP_E2E_MODEL     model name for the chosen SDK (Claude default: claude-haiku-4-5; Codex default: gpt-5-mini)
// env CHATMCP_E2E_TASK      "ping" (default) — alice posts @bob asking for PINGBACK, bob replies
//                           "multiply" — alice asks bob for his secret number (in MY_SECRET env on bob),
//                                        bob replies with it, alice multiplies by her secret (MY_SECRET on alice)
//                                        and posts the product. Verifier asserts product = a*b.
// env MY_SECRET             only used by task=multiply; per-agent integer secret
//
// Spawns the chosen SDK, configures `chatmcp` as a stdio MCP server, runs the
// role-specific prompt to completion, and exits 0 on a "DONE" final response
// or 3 otherwise.

const role = process.argv[2];
if (role !== "alice" && role !== "bob") {
  console.error(`usage: agent.mjs alice|bob (got: ${role})`);
  process.exit(2);
}

const sdkChoice = process.env.CHATMCP_E2E_SDK ?? "claude";
if (sdkChoice !== "claude" && sdkChoice !== "codex") {
  console.error(`CHATMCP_E2E_SDK must be claude|codex (got: ${sdkChoice})`);
  process.exit(2);
}

const task = process.env.CHATMCP_E2E_TASK ?? "ping";
if (task !== "ping" && task !== "multiply" && task !== "autochat") {
  console.error(`CHATMCP_E2E_TASK must be ping|multiply|autochat (got: ${task})`);
  process.exit(2);
}

const dbPath = process.env.CHATMCP_DB_PATH ?? "/data/chat.db";

const PROMPTS = {
  ping: {
    alice: `You are an automated test agent named "alice". Use the chatmcp MCP server to coordinate with another agent named "bob".

Goal: post a message in topic "demo" that mentions @bob, then poll for a reply containing the literal token "PINGBACK".

Steps:
1. Call mcp__chat__chat_join with topic="demo".
2. Call mcp__chat__chat_post with topic="demo" and body="hello @bob, please respond with PINGBACK".
3. Loop (call chat_read repeatedly, no waiting between calls — the tool returns immediately):
   - Call mcp__chat__chat_read with topic="demo".
   - If any message body contains the substring "PINGBACK" AND that message's author_name is "bob": stop and respond with the single word DONE.
   - Otherwise call chat_read again.
4. Stop after at most 40 chat_read calls regardless. If you stop without seeing PINGBACK, respond with TIMEOUT.

Do not invent output. Use only the chat tools. Do not ask questions.`,

    bob: `You are an automated test agent named "bob". Use the chatmcp MCP server to coordinate with another agent named "alice".

Goal: detect when alice mentions you and asks for PINGBACK, then post "PINGBACK" in topic "demo".

Steps:
1. Call mcp__chat__chat_join with topic="demo".
2. Loop (call chat_inbox repeatedly, no waiting between calls):
   - Call mcp__chat__chat_inbox.
   - If you have any unread mention whose body contains "PINGBACK":
     a. Call mcp__chat__chat_post with topic="demo" and body="PINGBACK".
     b. Stop and respond with the single word DONE.
   - Otherwise call chat_inbox again.
3. Stop after at most 40 chat_inbox calls. If you stop without seeing the mention, respond with TIMEOUT.

Do not invent output. Use only the chat tools. Do not ask questions.`,
  },

  autochat: {
    alice: `You are an automated test agent named "alice". Use the chatmcp MCP server to coordinate with another agent named "bob".

Goal: post a message in topic "demo" mentioning @bob, then BLOCK on chat_wait until bob's reply arrives.

CRITICAL — read this carefully:
- Do NOT call mcp__chat__chat_read or mcp__chat__chat_inbox in a polling loop.
- Use mcp__chat__chat_wait as your ONLY mechanism to receive new messages.
- chat_wait blocks server-side and returns when new messages arrive (or times out). It uses zero model tokens during the wait.

Steps:
1. Call mcp__chat__chat_join with topic="demo".
2. Call mcp__chat__chat_post with topic="demo" and body="hello @bob, please reply PINGBACK". Note the returned message_id; call it M.
3. Call mcp__chat__chat_wait with topic="demo", since=M, and timeout_s=20.
4. If chat_wait returns messages and any has author_name="bob" with body containing "PINGBACK": stop and respond with the single word DONE.
5. If chat_wait timed out (timed_out=true) without bob's reply: call chat_wait one more time with the same parameters (timeout_s=20). If it times out again, respond TIMEOUT.

Do not invent output. Use only the chat_join, chat_post, and chat_wait tools. Do not ask questions.`,

    bob: `You are an automated test agent named "bob". Use the chatmcp MCP server to coordinate with another agent named "alice".

Goal: detect alice's mention asking for PINGBACK, reply with PINGBACK, then exit.

CRITICAL — read this carefully:
- Do NOT call mcp__chat__chat_read or mcp__chat__chat_inbox in a polling loop.
- Use mcp__chat__chat_wait as your ONLY mechanism to receive new messages.
- chat_wait blocks server-side and returns when new messages arrive (or times out). It uses zero model tokens during the wait.

Steps:
1. Call mcp__chat__chat_join with topic="demo".
2. Call mcp__chat__chat_wait with topic="demo", since=0, and timeout_s=20.
3. If chat_wait returns messages and any has author_name="alice" with body containing "PINGBACK":
   a. Call mcp__chat__chat_post with topic="demo" and body="PINGBACK".
   b. Stop and respond with the single word DONE.
4. If chat_wait timed out without alice's mention: call chat_wait one more time (timeout_s=20). If it times out again, respond TIMEOUT.

Do not invent output. Use only the chat_join, chat_post, and chat_wait tools.`,
  },

  multiply: {
    alice: () => {
      const secret = requireSecret();
      return `You are an automated test agent named "alice". Your private secret number is ${secret}. The other agent "bob" has his own secret number that you do NOT know — you must ask him for it.

Goal: get bob's secret number, multiply it by yours (${secret}), post the product.

Steps:
1. Call mcp__chat__chat_join with topic="math".
2. Call mcp__chat__chat_post with topic="math" and body="hi @bob, what is your secret number? please reply with just the number and nothing else.".
3. Loop (call chat_read repeatedly, no waiting between calls):
   - Call mcp__chat__chat_read with topic="math".
   - Examine messages whose author_name is "bob". Extract the FIRST integer that appears in any such message's body. (Bob's secret is a small positive integer.)
   - If you found bob's number N:
     a. Compute the product P = ${secret} * N.
     b. Call mcp__chat__chat_post with topic="math" and body="product=" + P. Use exactly that format: the literal word "product=" followed by the integer, no spaces, no other text.
     c. Stop and respond with the single word DONE.
4. Stop after at most 60 chat_read calls. If you couldn't find bob's number, respond TIMEOUT.

Do not invent bob's number. Do not post any other messages besides the question and the product. Use only the chat tools. Do not ask questions.`;
    },

    bob: () => {
      const secret = requireSecret();
      return `You are an automated test agent named "bob". Your private secret number is ${secret}. Another agent "alice" will ask you for it.

Goal: reply to alice with your secret number when she asks, then wait until she posts a product, then exit.

Steps:
1. Call mcp__chat__chat_join with topic="math".
2. Loop (call chat_inbox repeatedly, no waiting between calls):
   - Call mcp__chat__chat_inbox.
   - If you have an unread mention from alice asking for your secret number:
     a. Call mcp__chat__chat_post with topic="math" and body="${secret}". Use exactly that body: just the number, no other text.
     b. Break out of this loop and go to step 3.
3. Now loop (call chat_read repeatedly, no waiting between calls):
   - Call mcp__chat__chat_read with topic="math".
   - If any message's author_name is "alice" AND its body starts with "product=":
     - Stop and respond with the single word DONE.
4. Stop after at most 60 total tool calls. If you didn't see the product, respond TIMEOUT.

Do not reveal or guess alice's number. Do not invent values. Use only the chat tools.`;
    },
  },
};

function requireSecret() {
  const s = process.env.MY_SECRET;
  const n = parseInt(s ?? "", 10);
  if (!Number.isInteger(n) || n <= 0) {
    console.error(`task=multiply requires MY_SECRET to be a positive integer (got: ${JSON.stringify(s)})`);
    process.exit(2);
  }
  return n;
}

const promptValue = PROMPTS[task]?.[role];
if (!promptValue) {
  console.error(`no prompt defined for task=${task} role=${role}`);
  process.exit(2);
}
const prompt = typeof promptValue === "function" ? promptValue() : promptValue;
const startedAt = Date.now();

if (sdkChoice === "claude") {
  await runClaude(role, prompt);
} else {
  await runCodex(role, prompt);
}

// ---------------- Claude Agent SDK driver ----------------

async function runClaude(role, prompt) {
  if (!process.env.ANTHROPIC_API_KEY) {
    console.error("ANTHROPIC_API_KEY is required for sdk=claude");
    process.exit(2);
  }
  const { query } = await import("@anthropic-ai/claude-agent-sdk");
  const model = process.env.CHATMCP_E2E_MODEL ?? "claude-haiku-4-5";

  const session = query({
    prompt,
    options: {
      model,
      tools: [],
      mcpServers: {
        chat: {
          type: "stdio",
          command: "/usr/local/bin/chatmcp",
          args: [],
          env: {
            CHATMCP_AGENT_NAME: role,
            CHATMCP_DB_PATH: dbPath,
            CHATMCP_POLL_MS: "100",
          },
        },
      },
      allowedTools: [
        "mcp__chat__chat_whoami",
        "mcp__chat__chat_join",
        "mcp__chat__chat_post",
        "mcp__chat__chat_read",
        "mcp__chat__chat_inbox",
        "mcp__chat__chat_wait",
      ],
      permissionMode: "bypassPermissions",
      maxTurns: 120,
      cwd: "/app",
    },
  });

  let finalText = "";
  let finalCostUSD = null;
  let toolCalls = 0;

  for await (const msg of session) {
    if (msg.type === "assistant") {
      for (const block of msg.message?.content ?? []) {
        if (block.type === "text") finalText = block.text;
        if (block.type === "tool_use") toolCalls++;
      }
    } else if (msg.type === "result") {
      finalCostUSD = msg.total_cost_usd ?? null;
    }
  }

  reportAndExit(role, "claude", model, finalText, toolCalls, finalCostUSD);
}

// ---------------- Codex SDK driver ----------------

async function runCodex(role, prompt) {
  if (!process.env.OPENAI_API_KEY) {
    console.error("OPENAI_API_KEY is required for sdk=codex");
    process.exit(2);
  }
  const { Codex } = await import("@openai/codex-sdk");
  const model = process.env.CHATMCP_E2E_MODEL ?? "gpt-5-mini";

  // Codex CLI reads MCP servers from ~/.codex/config.toml. The SDK's `config`
  // option flattens nested objects into `--config key=value` overrides, which
  // it then passes to the CLI as TOML literals. Pass our chatmcp wiring that way.
  const codex = new Codex({
    apiKey: process.env.OPENAI_API_KEY,
    config: {
      mcp_servers: {
        chat: {
          command: "/usr/local/bin/chatmcp",
          args: [],
          env: {
            CHATMCP_AGENT_NAME: role,
            CHATMCP_DB_PATH: dbPath,
            CHATMCP_POLL_MS: "100",
          },
        },
      },
    },
  });

  const thread = codex.startThread({
    model,
    // Codex auto-approves MCP tool calls only when approval_policy="never"
    // AND the sandbox grants full disk write — read-only and workspace-write
    // both fall through to "ask the user", which auto-cancels in headless mode.
    // We're inside an ephemeral Docker container with nothing to protect, so
    // danger-full-access is fine and lets the agent proceed without prompts.
    sandboxMode: "danger-full-access",
    approvalPolicy: "never",
    skipGitRepoCheck: true,
    workingDirectory: "/app",
    // gpt-5-mini at "low" reasoning is plenty for the polling task.
    // gpt-5-nano was tried at "low" and "medium" — both kept inventing
    // a non-existent `read_mcp_resource` tool instead of calling the real
    // `mcp__chat__*` tools, so it's not a viable default.
    modelReasoningEffort: process.env.CODEX_REASONING_EFFORT ?? "low",
    networkAccessEnabled: true,
  });

  // Stream events so we can log each tool call as it happens. Helpful for
  // debugging why a Codex run took N turns and didn't end with DONE.
  const streamed = await thread.runStreamed(prompt);

  let toolCalls = 0;
  let usage = null;
  let finalText = "";
  for await (const event of streamed.events) {
    switch (event.type) {
      case "item.completed": {
        const item = event.item;
        if (item.type === "mcp_tool_call") {
          toolCalls++;
          const args = JSON.stringify(item.arguments).slice(0, 180);
          let extra = "";
          if (item.status === "failed" && item.error) {
            extra = ` error=${JSON.stringify(item.error.message).slice(0, 240)}`;
          } else if (item.result?.structured_content !== undefined) {
            extra = ` result=${JSON.stringify(item.result.structured_content).slice(0, 240)}`;
          }
          console.log(`[${role}] tool: ${item.tool} args=${args} status=${item.status}${extra}`);
        } else if (item.type === "agent_message") {
          finalText = item.text;
        } else if (item.type === "error") {
          console.error(`[${role}] item.error: ${item.message}`);
        }
        break;
      }
      case "turn.completed":
        usage = event.usage;
        break;
      case "turn.failed":
        console.error(`[${role}] turn.failed: ${event.error.message}`);
        break;
      case "error":
        console.error(`[${role}] thread.error: ${event.message}`);
        break;
    }
  }

  let costStr = null;
  if (usage) {
    costStr = `tokens(in=${usage.input_tokens} cached=${usage.cached_input_tokens} out=${usage.output_tokens} reason=${usage.reasoning_output_tokens})`;
  }
  reportAndExit(role, "codex", model, finalText, toolCalls, costStr);
}

// ---------------- shared exit reporter ----------------

function reportAndExit(role, sdk, model, finalText, toolCalls, costInfo) {
  const elapsed = ((Date.now() - startedAt) / 1000).toFixed(1);
  const costStr =
    costInfo == null ? "n/a" : typeof costInfo === "number" ? `$${costInfo.toFixed(4)}` : costInfo;
  console.log(
    `[${role}] sdk=${sdk} model=${model} done in ${elapsed}s, ${toolCalls} tool calls, cost=${costStr}, last text="${(finalText ?? "").slice(0, 120)}"`,
  );
  // Accept DONE anywhere in the final text — Claude in particular tends to
  // wrap the keyword in commentary like "Excellent! ... DONE". The
  // authoritative success check is the run-*.sh DB assertion, this gate is
  // just a hint that the agent thought it was finished.
  if (!/\bDONE\b/i.test(finalText ?? "")) {
    console.error(`[${role}] did not finish with DONE; final text was "${finalText}"`);
    process.exit(3);
  }
}
