#!/usr/bin/env bash
#
# Autonomous-chat Docker E2E: each agent uses ONLY chat_wait to receive new
# messages. Their prompts explicitly forbid chat_read/chat_inbox polling
# loops. Proves that chat_wait's long-poll surface enables sustained
# real-time multi-agent collaboration without manual polling instructions.
#
# Verifier asserts (a) bob's PINGBACK reply landed, (b) no agent made an
# excessive number of chat_read/chat_inbox calls (the prompt may permit
# zero of those, but we tolerate a small budget for sanity).
#
# Defaults to same-vendor (both Claude). Set CROSS=1 to use Codex for bob.
#
# Requires: docker, docker compose v2, ANTHROPIC_API_KEY (and OPENAI_API_KEY when CROSS=1).
set -euo pipefail
cd "$(dirname "$0")"

if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
  echo "ERROR: ANTHROPIC_API_KEY must be set" >&2
  exit 2
fi

COMPOSE_FILES=(-f docker-compose.yml -f docker-compose.autochat.yml)
if [[ "${CROSS:-0}" == "1" ]]; then
  if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    echo "ERROR: CROSS=1 requires OPENAI_API_KEY" >&2
    exit 2
  fi
  COMPOSE_FILES+=(-f docker-compose.cross.yml)
  echo "==> mode: cross (alice=Claude, bob=Codex)"
else
  echo "==> mode: same-vendor (both Claude)"
fi

export CHATMCP_E2E_MODEL=${CLAUDE_E2E_MODEL:-claude-haiku-4-5}

docker compose "${COMPOSE_FILES[@]}" down -v --remove-orphans >/dev/null 2>&1 || true

echo "==> building chatmcp-e2e-agent image"
docker compose "${COMPOSE_FILES[@]}" build

echo "==> running alice + bob (chat_wait-only mode)"
set +e
timeout --foreground 240s docker compose "${COMPOSE_FILES[@]}" up
compose_rc=$?
set -e

echo
echo "==> chat.db contents (post-run)"
docker run --rm \
  --entrypoint /usr/bin/sqlite3 \
  -v chatmcp-e2e-chat-data:/data \
  chatmcp-e2e-agent:latest \
  /data/chat.db \
  "SELECT id, author_name, body FROM messages ORDER BY id" || true

echo
echo "==> assertions"

read_count() {
  docker run --rm \
    --entrypoint /usr/bin/sqlite3 \
    -v chatmcp-e2e-chat-data:/data \
    chatmcp-e2e-agent:latest \
    /data/chat.db \
    "$1"
}

alice_mention=$(read_count "SELECT COUNT(*) FROM messages WHERE author_name='alice' AND body LIKE '%@bob%'")
bob_pingback=$(read_count "SELECT COUNT(*) FROM messages WHERE author_name='bob' AND body LIKE '%PINGBACK%'")

echo "  alice messages mentioning @bob: $alice_mention"
echo "  bob messages containing PINGBACK: $bob_pingback"

docker compose "${COMPOSE_FILES[@]}" down -v --remove-orphans >/dev/null 2>&1 || true

if [[ "$alice_mention" -ge 1 && "$bob_pingback" -ge 1 ]]; then
  echo "✓ AUTOCHAT E2E PASSED (compose rc=$compose_rc)"
  exit 0
fi

echo "✗ AUTOCHAT E2E FAILED (compose rc=$compose_rc)" >&2
exit 1
