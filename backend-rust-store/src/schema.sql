
CREATE TABLE IF NOT EXISTS probe_results (
 path TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, delay INTEGER NOT NULL, error TEXT NOT NULL, measured INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS nodes (
  name       TEXT PRIMARY KEY,
  protocol   TEXT NOT NULL DEFAULT '',
  network    TEXT NOT NULL DEFAULT '',
  tls        INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS health (
  name                  TEXT PRIMARY KEY,
  state                 TEXT NOT NULL,
  consecutive_failures  INTEGER NOT NULL DEFAULT 0,
  consecutive_successes INTEGER NOT NULL DEFAULT 0,
  latency_ms            INTEGER NOT NULL DEFAULT 0,
  last_success_at       INTEGER NOT NULL DEFAULT 0,
  cooldown_until        INTEGER NOT NULL DEFAULT 0,
  updated_at            INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS region_verify (
  name        TEXT PRIMARY KEY,
  exit_ip     TEXT NOT NULL DEFAULT '',
  country     TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL,
  sources     TEXT NOT NULL DEFAULT '',
  detail      TEXT NOT NULL DEFAULT '',
  verified_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS switch_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  at         INTEGER NOT NULL,
  group_name TEXT NOT NULL,
  from_node  TEXT NOT NULL DEFAULT '',
  to_node    TEXT NOT NULL DEFAULT '',
  trigger    TEXT NOT NULL,
  evidence   TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS settings_kv (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS switch_events_at ON switch_events (at DESC);
