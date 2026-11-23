package main

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"time"
)

// SQLite implementation of the traffic-history storage boundary. The caller
// supplies snapshots and timestamps; no kernel or clock is consulted here.
func (s *goStoreBackend) recordTraffic(session string, snapshot kernelTraffic, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Old kernel sessions can never contribute another sample. Their history
	// remains, but their cumulative counter baselines are no longer needed.
	if _, err = tx.Exec("DELETE FROM traffic_checkpoints WHERE session <> ?", session); err != nil {
		return err
	}
	read, err := tx.Prepare("SELECT upload, download FROM traffic_checkpoints WHERE connection_key = ?")
	if err != nil {
		return err
	}
	defer read.Close()
	checkpoint, err := tx.Prepare(`INSERT INTO traffic_checkpoints VALUES(?,?,?,?)
 ON CONFLICT(connection_key) DO UPDATE SET upload=excluded.upload, download=excluded.download`)
	if err != nil {
		return err
	}
	defer checkpoint.Close()
	write, err := tx.Prepare(`INSERT INTO traffic_hours VALUES(?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(bucket,connection_key) DO UPDATE SET
 upload=traffic_hours.upload+excluded.upload, download=traffic_hours.download+excluded.download,
 last_seen=MAX(traffic_hours.last_seen,excluded.last_seen),
 app_key=excluded.app_key, app=excluded.app, target=excluded.target, chain=excluded.chain`)
	if err != nil {
		return err
	}
	defer write.Close()
	for _, c := range snapshot.Connections {
		if c.ID == "" {
			continue
		}
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(session+"\x00"+c.ID+"\x00"+c.Start)))
		var previousUp, previousDown int64
		err = read.QueryRow(key).Scan(&previousUp, &previousDown)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		up, down := counterDelta(c.Upload, previousUp), counterDelta(c.Download, previousDown)
		appKey, name := trafficApp(c.Metadata)
		if _, err = write.Exec(now.Format("2006-01-02T15"), key, appKey, name,
			connectionTarget(c.Metadata), c.Metadata.Network, connectionChain(c.Chains),
			up, down, now.Unix(), now.Unix()); err != nil {
			return err
		}
		if _, err = checkpoint.Exec(key, session, max(c.Upload, 0), max(c.Download, 0)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *goStoreBackend) readTrafficHistory(report trafficHistoryReport, appKey string) (trafficHistoryReport, error) {
	report.Rows = []trafficHistoryRow{}
	where := " WHERE bucket LIKE ?"
	args := []any{report.Date + "%"}
	if appKey != "" {
		where += " AND app_key = ?"
		args = append(args, appKey)
	}
	group := "app_key"
	if report.Group == "connection" {
		group = "connection_key"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	err = tx.QueryRow(`SELECT COALESCE(SUM(upload),0),COALESCE(SUM(download),0),COUNT(DISTINCT connection_key),COUNT(DISTINCT `+group+`) FROM traffic_hours`+where, args...).Scan(
		&report.UploadTotal, &report.DownloadTotal, &report.ConnectionCount, &report.RowCount)
	if err != nil {
		return report, err
	}
	rows, err := tx.Query(`SELECT `+group+`,MAX(app),MAX(app_key),MAX(target),MAX(network),MAX(chain),
 SUM(upload),SUM(download),COUNT(DISTINCT connection_key),MIN(first_seen),MAX(last_seen)
 FROM traffic_hours`+where+` GROUP BY `+group+` ORDER BY SUM(upload)+SUM(download) DESC, `+group+` LIMIT ? OFFSET ?`, append(args, report.Limit, report.Offset)...)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var row trafficHistoryRow
		if err = rows.Scan(&row.ID, &row.App, &row.AppKey, &row.Target, &row.Network, &row.Chain, &row.Upload, &row.Download, &row.Connections, &row.FirstSeen, &row.LastSeen); err != nil {
			rows.Close()
			return report, err
		}
		if report.Group == "app" {
			row.Target = ""
			row.Network = ""
			row.Chain = ""
		}
		report.Rows = append(report.Rows, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	report.HasMore = int64(report.Offset+len(report.Rows)) < report.RowCount
	return report, tx.Commit()
}
