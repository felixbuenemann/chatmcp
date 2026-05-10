#!/usr/bin/env bash
#
# Multi-turn Docker E2E: alice and bob each have a private secret integer.
# Alice asks bob for his secret, bob replies with it, alice multiplies by
# her own secret and posts "product=<N>" to topic "math". Verifier reads
# the SQLite DB and asserts the posted product equals alice_secret * bob_secret.
#
# This is the only e2e that exercises true bidirectional reasoning — each
# agent has private state that must be communicated through the chat to
# arrive at the right answer.
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

# Random secrets in [2, 9] keep the math trivially within any model's reach
# (we are testing the conversation flow, not arithmetic). Allow the caller
# to pin them for reproducibility.
ALICE_SECRET=${ALICE_SECRET:-$((RANDOM % 8 + 2))}
BOB_SECRET=${BOB_SECRET:-$((RANDOM % 8 + 2))}
EXPECTED_PRODUCT=$((ALICE_SECRET * BOB_SECRET))
export ALICE_SECRET BOB_SECRET

echo "==> secrets: alice=$ALICE_SECRET, bob=$BOB_SECRET, expected product=$EXPECTED_PRODUCT"

COMPOSE_FILES=(-f docker-compose.yml -f docker-compose.multiply.yml)
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

echo "==> running alice + bob"
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
echo "==> verifying product"

# Pull alice's product message (last one if multiple, just in case).
product_body=$(docker run --rm \
  --entrypoint /usr/bin/sqlite3 \
  -v chatmcp-e2e-chat-data:/data \
  chatmcp-e2e-agent:latest \
  /data/chat.db \
  "SELECT body FROM messages WHERE author_name='alice' AND body LIKE 'product=%' ORDER BY id DESC LIMIT 1" 2>/dev/null || true)

# Bob's number (sanity check — verifies bob actually transmitted his secret).
bob_number=$(docker run --rm \
  --entrypoint /usr/bin/sqlite3 \
  -v chatmcp-e2e-chat-data:/data \
  chatmcp-e2e-agent:latest \
  /data/chat.db \
  "SELECT body FROM messages WHERE author_name='bob' AND body GLOB '[0-9]*' ORDER BY id ASC LIMIT 1" 2>/dev/null || true)

# Cleanup before assertion exit so failures still tear down compose.
docker compose "${COMPOSE_FILES[@]}" down -v --remove-orphans >/dev/null 2>&1 || true

echo "  bob's transmitted number: '$bob_number' (expected '$BOB_SECRET')"
echo "  alice's product message:  '$product_body' (expected 'product=$EXPECTED_PRODUCT')"

# Extract the integer from alice's "product=N" message.
posted_product=""
if [[ "$product_body" =~ ^product=([0-9]+)$ ]]; then
  posted_product="${BASH_REMATCH[1]}"
fi

ok=1
if [[ "$bob_number" != "$BOB_SECRET" ]]; then
  echo "  ✗ bob did not transmit the correct secret"
  ok=0
fi
if [[ -z "$posted_product" ]]; then
  echo "  ✗ alice did not post a 'product=<N>' message"
  ok=0
elif [[ "$posted_product" != "$EXPECTED_PRODUCT" ]]; then
  echo "  ✗ posted product $posted_product != expected $EXPECTED_PRODUCT"
  ok=0
fi

if [[ "$ok" == "1" ]]; then
  echo "✓ MULTIPLY E2E PASSED (compose rc=$compose_rc, $ALICE_SECRET × $BOB_SECRET = $EXPECTED_PRODUCT)"
  exit 0
fi

echo "✗ MULTIPLY E2E FAILED (compose rc=$compose_rc)" >&2
exit 1
