
CREATE TABLE IF NOT EXISTS traffic_checkpoints (
 connection_key TEXT PRIMARY KEY, session TEXT NOT NULL,
 upload INTEGER NOT NULL, download INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS traffic_hours (
 bucket TEXT NOT NULL, connection_key TEXT NOT NULL,
 app_key TEXT NOT NULL, app TEXT NOT NULL, target TEXT NOT NULL,
 network TEXT NOT NULL, chain TEXT NOT NULL,
 upload INTEGER NOT NULL, download INTEGER NOT NULL,
 first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL,
 PRIMARY KEY(bucket, connection_key)
);
CREATE INDEX IF NOT EXISTS traffic_hours_app ON traffic_hours(app_key, bucket);
