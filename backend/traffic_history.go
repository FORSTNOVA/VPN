package main

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Hourly observations are the source for all four calendar views. Checkpoints
// and increments commit together, so a retry cannot count the same bytes twice.
const trafficHistorySchema = `
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
`

func trafficApp(m kernelMetadata) (string, string) {
	name := connectionProcess(m)
	if p := strings.TrimSpace(m.ProcessPath); p != "" {
		return "path:" + p, name
	}
	if name != "" {
		return "process:" + name, name
	}
	if m.UID != "" && m.UID != "-1" {
		return "uid:" + string(m.UID), "UID " + string(m.UID)
	}
	return "unknown", "未识别应用"
}

func counterDelta(current, previous int64) int64 {
	if current < 0 {
		return 0
	}
	if current < previous {
		return current // A reset starts a new counter, never a negative delta.
	}
	return current - previous
}

func (s *store) recordTraffic(session string, snapshot kernelTraffic, now time.Time) error {
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

func (a *app) startTrafficHistoryLocked() {
	if a.store == nil || a.trafficHistoryCancel != nil {
		return
	}
	cancel := make(chan struct{})
	a.trafficHistoryCancel = cancel
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-cancel:
				return
			case <-ticker.C:
				a.mu.Lock()
				// A stopped/replaced kernel must not be sampled by its old loop.
				if a.trafficHistoryCancel != cancel {
					a.mu.Unlock()
					return
				}
				a.collectTrafficHistoryLocked()
				a.mu.Unlock()
			}
		}
	}()
}

func (a *app) stopTrafficHistoryLocked() {
	if a.trafficHistoryCancel != nil {
		close(a.trafficHistoryCancel)
		a.trafficHistoryCancel = nil
	}
}

func (a *app) collectTrafficHistoryLocked() {
	if a.store == nil || !a.kernelRunningLocked() {
		return
	}
	snapshot, err := a.kernelTraffic()
	if err != nil {
		a.trafficHistoryError = "无法读取连接快照，历史采样暂时中断"
		return
	}
	err = a.store.recordTraffic(a.started.Format(time.RFC3339Nano), snapshot, time.Now())
	if err != nil {
		a.trafficHistoryError = "无法保存流量统计，请检查本地数据库与磁盘空间"
		return
	}
	a.trafficHistoryError = ""
}

type trafficHistoryRow struct {
	ID          string `json:"id"`
	App         string `json:"app"`
	AppKey      string `json:"appKey"`
	Target      string `json:"target"`
	Network     string `json:"network"`
	Chain       string `json:"chain"`
	Upload      int64  `json:"upload"`
	Download    int64  `json:"download"`
	Connections int64  `json:"connections"`
	FirstSeen   int64  `json:"firstSeen"`
	LastSeen    int64  `json:"lastSeen"`
}

type trafficHistoryReport struct {
	Period          string              `json:"period"`
	Date            string              `json:"date"`
	Group           string              `json:"group"`
	Timezone        string              `json:"timezone"`
	UploadTotal     int64               `json:"uploadTotal"`
	DownloadTotal   int64               `json:"downloadTotal"`
	ConnectionCount int64               `json:"connectionCount"`
	RowCount        int64               `json:"rowCount"`
	Offset          int                 `json:"offset"`
	Limit           int                 `json:"limit"`
	HasMore         bool                `json:"hasMore"`
	Rows            []trafficHistoryRow `json:"rows"`
	SamplingError   string              `json:"samplingError"`
}

func historyDate(period, date string, now time.Time) (string, error) {
	layout := map[string]string{"hour": "2006-01-02T15", "day": "2006-01-02", "month": "2006-01", "year": "2006"}[period]
	if layout == "" {
		return "", fmt.Errorf("统计周期必须为 hour、day、month 或 year")
	}
	if date == "" {
		return now.Format(layout), nil
	}
	parsed, err := time.ParseInLocation(layout, date, now.Location())
	if err != nil || parsed.Format(layout) != date {
		return "", fmt.Errorf("日期格式与统计周期不符")
	}
	return date, nil
}

func (s *store) readTrafficHistory(report trafficHistoryReport, appKey string) (trafficHistoryReport, error) {
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

func (a *app) trafficHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	period, group := q.Get("period"), q.Get("group")
	if period == "" {
		period = "day"
	}
	if group == "" {
		group = "app"
	}
	date, err := historyDate(period, q.Get("date"), time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if group != "app" && group != "connection" {
		http.Error(w, "不支持的分组", http.StatusBadRequest)
		return
	}
	offset, limit := 0, 100
	for key, dest := range map[string]*int{"offset": &offset, "limit": &limit} {
		if value := q.Get(key); value != "" {
			*dest, err = strconv.Atoi(value)
			if err != nil || *dest < 0 || (key == "limit" && (*dest < 1 || *dest > 200)) {
				http.Error(w, "分页参数无效", http.StatusBadRequest)
				return
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store == nil {
		http.Error(w, "历史数据库不可用", http.StatusServiceUnavailable)
		return
	}
	report, err := a.store.readTrafficHistory(trafficHistoryReport{
		Period: period, Date: date, Group: group, Offset: offset, Limit: limit,
		Timezone: time.Now().Format("MST -07:00"), SamplingError: a.trafficHistoryError,
	}, q.Get("app"))
	if err != nil {
		http.Error(w, "无法读取历史统计", http.StatusInternalServerError)
		return
	}
	writeJSON(w, report)
}
