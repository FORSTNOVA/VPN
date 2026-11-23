package main

import (
	"log"
	"net/http"
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
	r := runSelectedTrafficProjection(trafficProjectionRequest{Op: trafficProjectionApp, Rows: []trafficProjectionFacts{trafficProjectionMetadata(m)}})
	return r.Text, r.Other
}

func counterDeltaGo(current, previous int64) int64 {
	if current < 0 {
		return 0
	}
	if current < previous {
		return current // A reset starts a new counter, never a negative delta.
	}
	return current - previous
}

func (a *app) startTrafficHistoryLocked() {
	r := a.runTrafficService(trafficServiceRequest{Op: "start"}, nil)
	if r.Err != nil {
		log.Print("traffic history start incomplete")
	}
}

func (a *app) stopTrafficHistoryLocked() {
	r := a.runTrafficService(trafficServiceRequest{Op: "stop"}, nil)
	if r.Err != nil {
		log.Print("traffic history stop incomplete")
	}
}

func (a *app) collectTrafficHistoryLocked() {
	r := a.runTrafficService(trafficServiceRequest{Op: "collect"}, nil)
	if r.Err != nil {
		log.Print("traffic history collect incomplete")
	}
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
	return projectionHistoryDate(period, date, now)
}

func (a *app) trafficHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	result := a.runTrafficService(trafficServiceRequest{Op: "query", Period: q.Get("period"), Date: q.Get("date"), Group: q.Get("group"), Offset: q.Get("offset"), Limit: q.Get("limit"), App: q.Get("app")}, nil)
	writeTrafficServiceResult(w, result)
}
