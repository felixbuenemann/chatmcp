CREATE TABLE schema_version (
  version    INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL
);

CREATE TABLE agents (
  id         INTEGER PRIMARY KEY,
  name       TEXT    NOT NULL UNIQUE,
  kind       TEXT    NOT NULL,
  pid        INTEGER NOT NULL,
  started_at INTEGER NOT NULL
);

CREATE TABLE topics (
  id         INTEGER PRIMARY KEY,
  name       TEXT    NOT NULL UNIQUE,
  created_by INTEGER NOT NULL REFERENCES agents(id),
  created_at INTEGER NOT NULL
);

CREATE TABLE memberships (
  topic_id             INTEGER NOT NULL REFERENCES topics(id),
  agent_id             INTEGER NOT NULL REFERENCES agents(id),
  joined_at            INTEGER NOT NULL,
  last_read_message_id INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (topic_id, agent_id)
);

CREATE TABLE messages (
  id            INTEGER PRIMARY KEY,
  topic_id      INTEGER NOT NULL REFERENCES topics(id),
  author_id     INTEGER NOT NULL REFERENCES agents(id),
  author_name   TEXT    NOT NULL,
  body          TEXT    NOT NULL,
  created_at_us INTEGER NOT NULL
);
CREATE INDEX idx_messages_topic_id ON messages(topic_id, id);

CREATE TABLE mentions (
  message_id         INTEGER NOT NULL REFERENCES messages(id),
  mentioned_agent_id INTEGER NOT NULL REFERENCES agents(id),
  dismissed_at       INTEGER,
  PRIMARY KEY (message_id, mentioned_agent_id)
);
CREATE INDEX idx_mentions_unread ON mentions(mentioned_agent_id, dismissed_at);
