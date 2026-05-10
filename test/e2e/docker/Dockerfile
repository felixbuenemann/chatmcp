# syntax=docker/dockerfile:1
# Build chatmcp from source, then bundle it into a Node image alongside the
# Claude Agent SDK driver script. The same image runs as both alice and bob;
# the role is selected via the container's command.

FROM golang:1.26-alpine AS chatmcp-build
WORKDIR /src
# Copy go.mod/go.sum first for better layer caching.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' \
    -o /out/chatmcp ./cmd/chatmcp

FROM node:22-slim
RUN apt-get update && \
    apt-get install -y --no-install-recommends sqlite3 ca-certificates tini && \
    rm -rf /var/lib/apt/lists/*
COPY --from=chatmcp-build /out/chatmcp /usr/local/bin/chatmcp

WORKDIR /app
COPY test/e2e/docker/package.json ./
RUN npm install --omit=dev --no-audit --no-fund
COPY test/e2e/docker/agent.mjs ./

# claude CLI and codex CLI both refuse to run with elevated permissions.
# Reuse the `node` user (uid 1000) from node:22-slim, give it a writable
# HOME and own /app + /data, and prepare config dirs for both CLIs.
RUN mkdir -p /home/node/.claude /home/node/.codex /data && \
    chown -R node:node /home/node /app /data
USER node
ENV HOME=/home/node

ENTRYPOINT ["/usr/bin/tini", "--", "node", "/app/agent.mjs"]
