package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// regionStatus records how much confidence a node's exit region has. Only
// verified nodes may enter a locked-region candidate pool.
const (
	regionUnverified  = "unverified"
	regionVerified    = "verified"
	regionSingle      = "single"
	regionConflict    = "conflict"
	regionUnreachable = "unreachable"
)

type regionRecord struct {
	Name       string
	ExitIP     string
	Country    string
	Status     string
	Sources    []string
	Detail     string
	VerifiedAt time.Time
}

type switchEvent struct {
	At       time.Time
	Group    string
	FromNode string
	ToNode   string
	Trigger  string
	Evidence string
}

type store struct {
	db *sql.DB
}

const storeSchema = `
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
`

func openStore(path string) (*store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection keeps writes serialized, which suits this volume and
	// avoids SQLite lock contention between the patrol loop and HTTP handlers.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(storeSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

func (s *store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func unixSeconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeFromUnix(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func (s *store) upsertNodes(nodes []proxyNode) error {
	now := unixSeconds(time.Now())
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	statement, err := tx.Prepare(`
		INSERT INTO nodes (name, protocol, network, tls, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			protocol = excluded.protocol,
			network = excluded.network,
			tls = excluded.tls,
			updated_at = excluded.updated_at`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, node := range nodes {
		tls := 0
		if node.TLS {
			tls = 1
		}
		if _, err := statement.Exec(node.Name, node.Type, node.Network, tls, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *store) saveRegion(record regionRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO region_verify (name, exit_ip, country, status, sources, detail, verified_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			exit_ip = excluded.exit_ip,
			country = excluded.country,
			status = excluded.status,
			sources = excluded.sources,
			detail = excluded.detail,
			verified_at = excluded.verified_at`,
		record.Name, record.ExitIP, record.Country, record.Status,
		strings.Join(record.Sources, ","), record.Detail, unixSeconds(record.VerifiedAt))
	return err
}

func (s *store) loadRegions() (map[string]regionRecord, error) {
	rows, err := s.db.Query(`SELECT name, exit_ip, country, status, sources, detail, verified_at FROM region_verify`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := map[string]regionRecord{}
	for rows.Next() {
		var record regionRecord
		var sources string
		var verifiedAt int64
		if err := rows.Scan(&record.Name, &record.ExitIP, &record.Country, &record.Status,
			&sources, &record.Detail, &verifiedAt); err != nil {
			return nil, err
		}
		if sources != "" {
			record.Sources = strings.Split(sources, ",")
		}
		record.VerifiedAt = timeFromUnix(verifiedAt)
		records[record.Name] = record
	}
	return records, rows.Err()
}

func (s *store) clearRegions() error {
	_, err := s.db.Exec(`DELETE FROM region_verify`)
	return err
}

func (s *store) saveHealth(record nodeHealth) error {
	_, err := s.db.Exec(`
		INSERT INTO health (name, state, consecutive_failures, consecutive_successes,
			latency_ms, last_success_at, cooldown_until, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			state = excluded.state,
			consecutive_failures = excluded.consecutive_failures,
			consecutive_successes = excluded.consecutive_successes,
			latency_ms = excluded.latency_ms,
			last_success_at = excluded.last_success_at,
			cooldown_until = excluded.cooldown_until,
			updated_at = excluded.updated_at`,
		record.Name, record.State, record.ConsecutiveFailures, record.ConsecutiveSuccesses,
		record.LatencyMs, unixSeconds(record.LastSuccessAt), unixSeconds(record.CooldownUntil),
		unixSeconds(record.UpdatedAt))
	return err
}

func (s *store) loadHealth() (map[string]nodeHealth, error) {
	rows, err := s.db.Query(`SELECT name, state, consecutive_failures, consecutive_successes,
		latency_ms, last_success_at, cooldown_until, updated_at FROM health`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := map[string]nodeHealth{}
	for rows.Next() {
		var record nodeHealth
		var lastSuccess, cooldownUntil, updatedAt int64
		if err := rows.Scan(&record.Name, &record.State, &record.ConsecutiveFailures,
			&record.ConsecutiveSuccesses, &record.LatencyMs, &lastSuccess, &cooldownUntil,
			&updatedAt); err != nil {
			return nil, err
		}
		record.LastSuccessAt = timeFromUnix(lastSuccess)
		record.CooldownUntil = timeFromUnix(cooldownUntil)
		record.UpdatedAt = timeFromUnix(updatedAt)
		records[record.Name] = record
	}
	return records, rows.Err()
}

func (s *store) clearHealth() error {
	_, err := s.db.Exec(`DELETE FROM health`)
	return err
}

func (s *store) appendSwitchEvent(event switchEvent) error {
	_, err := s.db.Exec(`
		INSERT INTO switch_events (at, group_name, from_node, to_node, trigger, evidence)
		VALUES (?, ?, ?, ?, ?, ?)`,
		unixSeconds(event.At), event.Group, event.FromNode, event.ToNode,
		event.Trigger, event.Evidence)
	return err
}

func (s *store) recentSwitchEvents(limit int) ([]switchEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT at, group_name, from_node, to_node, trigger, evidence
		FROM switch_events ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]switchEvent, 0, limit)
	for rows.Next() {
		var event switchEvent
		var at int64
		if err := rows.Scan(&at, &event.Group, &event.FromNode, &event.ToNode,
			&event.Trigger, &event.Evidence); err != nil {
			return nil, err
		}
		event.At = timeFromUnix(at)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *store) setValue(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO settings_kv (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *store) getValue(key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings_kv WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (s *store) saveHealthParams(params healthParams) error {
	body, err := params.encode()
	if err != nil {
		return err
	}
	return s.setValue("health_params", body)
}

func (s *store) loadHealthParams() (healthParams, error) {
	params := defaultHealthParams()
	body, found, err := s.getValue("health_params")
	if err != nil || !found {
		return params, err
	}
	if err := params.decode(body); err != nil {
		return defaultHealthParams(), fmt.Errorf("stored health parameters were unreadable: %w", err)
	}
	return params.normalized(), nil
}
