//go:build (windows && amd64 && (ruststore || rustcore)) || (android && cgo && rustcore && (amd64 || arm64))

package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
)

func (w *storeWriter) region(v regionRecord) {
	w.s(v.Name)
	w.s(v.ExitIP)
	w.s(v.Country)
	w.s(v.Status)
	w.count(len(v.Sources))
	for _, x := range v.Sources {
		w.s(x)
	}
	w.s(v.Detail)
	w.i(unixSeconds(v.VerifiedAt))
}
func (r *storeReader) region() (v regionRecord) {
	v.Name = r.s()
	v.ExitIP = r.s()
	v.Country = r.s()
	v.Status = r.s()
	n := r.count()
	for j := 0; j < n; j++ {
		v.Sources = append(v.Sources, r.s())
	}
	v.Detail = r.s()
	v.VerifiedAt = timeFromUnix(r.i())
	return
}
func (w *storeWriter) health(v nodeHealth) {
	w.s(v.Name)
	w.s(v.State)
	w.i(int64(v.ConsecutiveFailures))
	w.i(int64(v.ConsecutiveSuccesses))
	w.i(int64(v.LatencyMs))
	w.i(unixSeconds(v.LastSuccessAt))
	w.i(unixSeconds(v.CooldownUntil))
	w.i(unixSeconds(v.UpdatedAt))
}
func (r *storeReader) health() (v nodeHealth) {
	v.Name = r.s()
	v.State = r.s()
	v.ConsecutiveFailures = int(r.i())
	v.ConsecutiveSuccesses = int(r.i())
	v.LatencyMs = int(r.i())
	v.LastSuccessAt = timeFromUnix(r.i())
	v.CooldownUntil = timeFromUnix(r.i())
	v.UpdatedAt = timeFromUnix(r.i())
	return
}
func (w *storeWriter) event(v switchEvent) {
	w.i(unixSeconds(v.At))
	w.s(v.Group)
	w.s(v.FromNode)
	w.s(v.ToNode)
	w.s(v.Trigger)
	w.s(v.Evidence)
}
func (r *storeReader) event() (v switchEvent) {
	v.At = timeFromUnix(r.i())
	v.Group = r.s()
	v.FromNode = r.s()
	v.ToNode = r.s()
	v.Trigger = r.s()
	v.Evidence = r.s()
	return
}
func (w *storeWriter) probe(v probeRecord) {
	w.s(v.Path)
	w.s(v.Fingerprint)
	w.i(v.Delay)
	w.s(v.Error)
	w.i(v.Measured)
	w.s(v.Details)
}
func (r *storeReader) probe() (v probeRecord) {
	v.Path = r.s()
	v.Fingerprint = r.s()
	v.Delay = r.i()
	v.Error = r.s()
	v.Measured = r.i()
	v.Details = r.s()
	return
}
func (w *storeWriter) row(v trafficHistoryRow) {
	w.s(v.ID)
	w.s(v.App)
	w.s(v.AppKey)
	w.s(v.Target)
	w.s(v.Network)
	w.s(v.Chain)
	w.i(v.Upload)
	w.i(v.Download)
	w.i(v.Connections)
	w.i(v.FirstSeen)
	w.i(v.LastSeen)
}
func (r *storeReader) row() (v trafficHistoryRow) {
	v.ID = r.s()
	v.App = r.s()
	v.AppKey = r.s()
	v.Target = r.s()
	v.Network = r.s()
	v.Chain = r.s()
	v.Upload = r.i()
	v.Download = r.i()
	v.Connections = r.i()
	v.FirstSeen = r.i()
	v.LastSeen = r.i()
	return
}
func (w *storeWriter) report(v trafficHistoryReport) {
	w.s(v.Period)
	w.s(v.Date)
	w.s(v.Group)
	w.s(v.Timezone)
	w.i(v.UploadTotal)
	w.i(v.DownloadTotal)
	w.i(v.ConnectionCount)
	w.i(v.RowCount)
	w.i(int64(v.Offset))
	w.i(int64(v.Limit))
	w.flag(v.HasMore)
	w.count(len(v.Rows))
	for _, row := range v.Rows {
		w.row(row)
	}
	w.s(v.SamplingError)
}
func (r *storeReader) report() (v trafficHistoryReport) {
	v.Period = r.s()
	v.Date = r.s()
	v.Group = r.s()
	v.Timezone = r.s()
	v.UploadTotal = r.i()
	v.DownloadTotal = r.i()
	v.ConnectionCount = r.i()
	v.RowCount = r.i()
	v.Offset = int(r.i())
	v.Limit = int(r.i())
	v.HasMore = r.flag()
	n := r.count()
	v.Rows = make([]trafficHistoryRow, 0, n)
	for j := 0; j < n; j++ {
		v.Rows = append(v.Rows, r.row())
	}
	v.SamplingError = r.s()
	return
}
func (s *rustStoreBackend) setValue(k, v string) error {
	var w storeWriter
	w.s(k)
	w.s(v)
	return s.write(1, w)
}
func (s *rustStoreBackend) getValue(k string) (string, bool, error) {
	var w storeWriter
	w.s(k)
	reply, e := s.execute(2, w)
	if e != nil {
		return "", false, e
	}
	r := storeReader{b: reply.payload}
	found := r.flag()
	v := ""
	if found {
		v = r.s()
	}
	return v, found, r.done()
}
func (s *rustStoreBackend) upsertNodesAt(nodes []proxyNode, at int64) error {
	var w storeWriter
	w.i(at)
	w.count(len(nodes))
	for _, n := range nodes {
		w.s(n.Name)
		w.s(n.Type)
		w.s(n.Network)
		w.flag(n.TLS)
	}
	return s.write(3, w)
}
func (s *rustStoreBackend) saveRegion(v regionRecord) error {
	var w storeWriter
	w.region(v)
	return s.write(4, w)
}
func (s *rustStoreBackend) loadRegions() (map[string]regionRecord, error) {
	reply, e := s.execute(5, storeWriter{})
	if e != nil {
		return nil, e
	}
	r := storeReader{b: reply.payload}
	out := map[string]regionRecord{}
	n := r.count()
	for j := 0; j < n; j++ {
		v := r.region()
		out[v.Name] = v
	}
	if e = r.done(); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *rustStoreBackend) clearRegions() error { return s.write(6, storeWriter{}) }
func (s *rustStoreBackend) saveHealth(v nodeHealth) error {
	var w storeWriter
	w.health(v)
	return s.write(7, w)
}
func (s *rustStoreBackend) loadHealth() (map[string]nodeHealth, error) {
	reply, e := s.execute(8, storeWriter{})
	if e != nil {
		return nil, e
	}
	r := storeReader{b: reply.payload}
	out := map[string]nodeHealth{}
	n := r.count()
	for j := 0; j < n; j++ {
		v := r.health()
		out[v.Name] = v
	}
	if e = r.done(); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *rustStoreBackend) clearHealth() error { return s.write(9, storeWriter{}) }
func (s *rustStoreBackend) appendSwitchEvent(v switchEvent) error {
	var w storeWriter
	w.event(v)
	return s.write(10, w)
}
func (s *rustStoreBackend) recentSwitchEvents(limit int) ([]switchEvent, error) {
	var w storeWriter
	w.i(int64(limit))
	reply, e := s.execute(11, w)
	if e != nil {
		return nil, e
	}
	r := storeReader{b: reply.payload}
	n := r.count()
	out := make([]switchEvent, 0, n)
	for j := 0; j < n; j++ {
		out = append(out, r.event())
	}
	if e = r.done(); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *rustStoreBackend) saveProbeRecord(v probeRecord) error {
	var w storeWriter
	w.probe(v)
	return s.write(12, w)
}
func (s *rustStoreBackend) loadProbeRecords() ([]probeRecord, error) {
	reply, e := s.execute(13, storeWriter{})
	if e != nil {
		return nil, e
	}
	r := storeReader{b: reply.payload}
	n := r.count()
	out := make([]probeRecord, 0, n)
	for j := 0; j < n; j++ {
		out = append(out, r.probe())
	}
	if e = r.done(); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *rustStoreBackend) recordTraffic(session string, snapshot kernelTraffic, now time.Time) error {
	var w storeWriter
	w.s(session)
	w.s(now.Format("2006-01-02T15"))
	w.i(now.Unix())
	w.count(len(snapshot.Connections))
	for _, c := range snapshot.Connections {
		w.flag(c.ID != "")
		w.s(fmt.Sprintf("%x", sha256.Sum256([]byte(session+"\x00"+c.ID+"\x00"+c.Start))))
		key, name := trafficApp(c.Metadata)
		w.s(key)
		w.s(name)
		w.s(connectionTarget(c.Metadata))
		w.s(c.Metadata.Network)
		w.s(connectionChain(c.Chains))
		w.i(c.Upload)
		w.i(c.Download)
	}
	return s.write(14, w)
}
func (s *rustStoreBackend) readTrafficHistory(report trafficHistoryReport, key string) (trafficHistoryReport, error) {
	var w storeWriter
	w.report(report)
	w.s(key)
	reply, e := s.execute(15, w)
	if len(reply.payload) == 0 {
		report.Rows = []trafficHistoryRow{}
		if e == nil {
			e = errStoreWire
		}
		return report, e
	}
	r := storeReader{b: reply.payload}
	out := r.report()
	if de := r.done(); de != nil {
		return report, errors.Join(e, de)
	}
	return out, e
}
