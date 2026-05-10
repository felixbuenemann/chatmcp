#!/usr/bin/env bash
#
# Cross-SDK Docker E2E: alice runs the Claude Agent SDK, bob runs the OpenAI
# Codex SDK. Both share one chatmcp database via a named Docker volume.
#
# Verifies that two coding-agent SDKs from different vendors can collaborate
# through chatmcp without protocol-level surprises.
#
# Requires: docker, docker compose v2, ANTHROPIC_API_KEY, OPENAI_API_KEY.
# Default Codex model is gpt-5-mini; override with CODEX_E2E_MODEL.
# Default Claude model is claude-haiku-4-5; override with CLAUDE_E2E_MODEL.
set -euo pipefail
cd "$(dirname "$0")"

if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
  echo "ERROR: ANTHROPIC_API_KEY must be set" >&2
  exit 2
fi
if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "ERROR: OPENAI_API_KEY must be set" >&2
  exit 2
fi

# CHATMCP_E2E_MODEL feeds docker-compose.yml's alice service via the existing
# default-substitution. Use CLAUDE_E2E_MODEL as an alias if set.
export CHATMCP_E2E_MODEL=${CLAUDE_E2E_MODEL:-claude-haiku-4-5}

COMPOSE_FILES=(-f docker-compose.yml -f docker-compose.cross.yml)

# Reset any prior state.
docker compose "${COMPOSE_FILES[@]}" down -v --remove-orphans >/dev/null 2>&1 || true

echo "==> building chatmcp-e2e-agent image"
docker compose "${COMPOSE_FILES[@]}" build

echo "==> running alice (Claude) + bob (Codex)"
set +e
timeout --foreground 480s docker compose "${COMPOSE_FILES[@]}" up
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
mention_rows=$(read_count "SELECT COUNT(*) FROM mentions")

echo "  alice (claude) messages mentioning @bob: $alice_mention"
echo "  bob   (codex)  messages containing PINGBACK: $bob_pingback"
echo "  mention rows: $mention_rows"

docker compose "${COMPOSE_FILES[@]}" down -v --remove-orphans >/dev/null 2>&1 || true

if [[ "$alice_mention" -ge 1 && "$bob_pingback" -ge 1 && "$mention_rows" -ge 1 ]]; then
  echo "✓ CROSS-SDK E2E PASSED (compose rc=$compose_rc)"
  exit 0
fi

echo "✗ CROSS-SDK E2E FAILED (compose rc=$compose_rc)" >&2
exit 1
