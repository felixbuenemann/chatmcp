#!/usr/bin/env bash
#
# Orchestrates the chatmcp Docker E2E:
#   1. Builds the chatmcp-e2e-agent image (chatmcp + Claude Agent SDK driver).
#   2. Brings up two containers (alice, bob) sharing one named volume.
#   3. Waits for both to exit (or aborts on first-exit and stops the other).
#   4. Asserts that the shared chat.db contains alice's mention and bob's PINGBACK.
#   5. Tears down compose + volume.
#
# Requires: docker, docker compose v2, ANTHROPIC_API_KEY in env.
# Default model is claude-haiku-4-5 (cheap, reliable on this task). Override
# with CHATMCP_E2E_MODEL.
#
# Usage:
#   ANTHROPIC_API_KEY=sk-ant-... ./run.sh
set -euo pipefail

cd "$(dirname "$0")"

if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
  echo "ERROR: ANTHROPIC_API_KEY must be set" >&2
  exit 2
fi

# Make sure we don't reuse stale state from a previous run.
docker compose down -v --remove-orphans >/dev/null 2>&1 || true

echo "==> building chatmcp-e2e-agent image"
docker compose build

echo "==> running alice + bob"
# Let both agents run to natural completion. Bob exits when he sees alice's
# mention and replies; alice exits when she sees bob's PINGBACK reply.
# Bound the whole exchange to 300s as a safety net for stuck agents.
set +e
timeout --foreground 300s docker compose up
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

echo "  alice messages mentioning @bob: $alice_mention"
echo "  bob messages containing PINGBACK: $bob_pingback"
echo "  mention rows: $mention_rows"

# Cleanup before exiting so a failure still tears the compose down.
docker compose down -v --remove-orphans >/dev/null 2>&1 || true

if [[ "$alice_mention" -ge 1 && "$bob_pingback" -ge 1 && "$mention_rows" -ge 1 ]]; then
  echo "✓ E2E PASSED (compose rc=$compose_rc)"
  exit 0
fi

echo "✗ E2E FAILED (compose rc=$compose_rc)" >&2
exit 1
