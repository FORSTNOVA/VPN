package main

import (
	"errors"
	"sort"
)

const serviceHealthMaxBytes = 16 << 20
const serviceHealthMaxItems = 100000

type serviceHealthWriter struct {
	b   []byte
	bad bool
}

func (w *serviceHealthWriter) n(v uint64) {
	if w.bad {
		return
	}
	if len(w.b) > serviceHealthMaxBytes-8 {
		w.bad = true
		return
	}
	w.b = registryInt(w.b, int64(v))
}
func (w *serviceHealthWriter) flag(v bool) {
	if w.bad {
		return
	}
	if len(w.b) >= serviceHealthMaxBytes {
		w.bad = true
		return
	}
	w.b = registryBool(w.b, v)
}
func (w *serviceHealthWriter) str(v string) {
	if w.bad {
		return
	}
	if len(v) > serviceHealthMaxBytes || len(w.b) > serviceHealthMaxBytes-8-len(v) {
		w.bad = true
		return
	}
	w.n(uint64(len(v)))
	if !w.bad {
		w.b = append(w.b, v...)
	}
}
func (w *serviceHealthWriter) count(p bool, n int) {
	if n > serviceHealthMaxItems {
		w.bad = true
		return
	}
	w.flag(p)
	w.n(uint64(n))
}
func (w *serviceHealthWriter) strings(v []string) {
	w.count(v != nil, len(v))
	for _, s := range v {
		w.str(s)
	}
}
func (w *serviceHealthWriter) params(p healthParams) {
	for _, v := range []int{p.ConnectTimeoutMs, p.PatrolIntervalSec, p.FailureThreshold, p.CooldownSec, p.MinDwellSec, p.RecoverySuccesses} {
		w.n(uint64(v))
	}
}
func (w *serviceHealthWriter) health(h serviceHealthRecord) {
	r := h.Record
	if !r.LastSuccessAt.IsZero() || !r.CooldownUntil.IsZero() || !r.UpdatedAt.IsZero() {
		w.bad = true
		return
	}
	w.str(r.Name)
	w.str(r.State)
	w.n(uint64(r.ConsecutiveFailures))
	w.n(uint64(r.ConsecutiveSuccesses))
	w.n(uint64(r.LatencyMs))
	w.n(h.LastSuccessToken)
	w.n(h.CooldownToken)
	w.n(h.UpdatedToken)
}
func (w *serviceHealthWriter) region(h serviceRegionRecord) {
	r := h.Record
	if !r.VerifiedAt.IsZero() {
		w.bad = true
		return
	}
	w.str(r.Name)
	w.str(r.ExitIP)
	w.str(r.Country)
	w.str(r.Status)
	w.strings(r.Sources)
	w.str(r.Detail)
	w.n(h.VerifiedToken)
}
func (w *serviceHealthWriter) healthMap(v map[string]serviceHealthRecord) {
	w.count(v != nil, len(v))
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w.str(k)
		w.health(v[k])
	}
}
func (w *serviceHealthWriter) regionMap(v map[string]serviceRegionRecord) {
	w.count(v != nil, len(v))
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w.str(k)
		w.region(v[k])
	}
}
func (w *serviceHealthWriter) job(j *regionJob) {
	w.flag(j != nil)
	if j == nil {
		return
	}
	w.flag(j.Running)
	for _, n := range []int{j.Total, j.Done, j.Verified, j.Skipped, j.Failed} {
		w.n(uint64(n))
	}
	w.str(j.StartedAt)
	w.str(j.FinishedAt)
	w.str(j.LastError)
}
func (w *serviceHealthWriter) state(s serviceHealthState) {
	w.params(s.Params)
	w.healthMap(s.Health)
	w.regionMap(s.Regions)
	w.job(s.Job)
	w.flag(s.Blocked)
	w.str(s.BlockReason)
	w.str(s.SweepNote)
	w.n(s.LastSwitchToken)
}
func (w *serviceHealthWriter) finish() (string, error) {
	if w.bad {
		return "", errors.New("invalid health domain encoding")
	}
	return string(w.b), nil
}

type serviceHealthReader struct{ quotaReader }

func (r *serviceHealthReader) flag() bool { return serviceReadBool(&r.quotaReader) }
func (r *serviceHealthReader) n() uint64  { return uint64(r.integer()) }
func (r *serviceHealthReader) platformInt() int {
	n := r.integer()
	v := int(n)
	if int64(v) != n {
		r.bad = true
	}
	return v
}
func (r *serviceHealthReader) count() (bool, int) {
	p := r.flag()
	n := r.n()
	if n > serviceHealthMaxItems || (!p && n != 0) {
		r.bad = true
		return false, 0
	}
	return p, int(n)
}
func (r *serviceHealthReader) strings() []string {
	p, n := r.count()
	if !p {
		return nil
	}
	v := make([]string, n)
	for i := range v {
		v[i] = r.str()
	}
	return v
}
func (r *serviceHealthReader) params() healthParams {
	return healthParams{ConnectTimeoutMs: r.platformInt(), PatrolIntervalSec: r.platformInt(), FailureThreshold: r.platformInt(), CooldownSec: r.platformInt(), MinDwellSec: r.platformInt(), RecoverySuccesses: r.platformInt()}
}
func (r *serviceHealthReader) health() serviceHealthRecord {
	return serviceHealthRecord{Record: nodeHealth{Name: r.str(), State: r.str(), ConsecutiveFailures: r.platformInt(), ConsecutiveSuccesses: r.platformInt(), LatencyMs: r.platformInt()}, LastSuccessToken: r.n(), CooldownToken: r.n(), UpdatedToken: r.n()}
}
func (r *serviceHealthReader) region() serviceRegionRecord {
	return serviceRegionRecord{Record: regionRecord{Name: r.str(), ExitIP: r.str(), Country: r.str(), Status: r.str(), Sources: r.strings(), Detail: r.str()}, VerifiedToken: r.n()}
}
func (r *serviceHealthReader) healthMap() map[string]serviceHealthRecord {
	p, n := r.count()
	if !p {
		return nil
	}
	v := make(map[string]serviceHealthRecord, n)
	last := ""
	for i := 0; i < n; i++ {
		k := r.str()
		if i > 0 && k <= last {
			r.bad = true
		}
		last = k
		v[k] = r.health()
	}
	return v
}
func (r *serviceHealthReader) regionMap() map[string]serviceRegionRecord {
	p, n := r.count()
	if !p {
		return nil
	}
	v := make(map[string]serviceRegionRecord, n)
	last := ""
	for i := 0; i < n; i++ {
		k := r.str()
		if i > 0 && k <= last {
			r.bad = true
		}
		last = k
		v[k] = r.region()
	}
	return v
}
func (r *serviceHealthReader) job() *regionJob {
	if !r.flag() {
		return nil
	}
	return &regionJob{Running: r.flag(), Total: r.platformInt(), Done: r.platformInt(), Verified: r.platformInt(), Skipped: r.platformInt(), Failed: r.platformInt(), StartedAt: r.str(), FinishedAt: r.str(), LastError: r.str()}
}
func (r *serviceHealthReader) state() serviceHealthState {
	return serviceHealthState{Params: r.params(), Health: r.healthMap(), Regions: r.regionMap(), Job: r.job(), Blocked: r.flag(), BlockReason: r.str(), SweepNote: r.str(), LastSwitchToken: r.n()}
}
func encodeServiceHealthState(s serviceHealthState) (string, error) {
	w := serviceHealthWriter{}
	w.state(s)
	return w.finish()
}
func decodeServiceHealthState(s string) (serviceHealthState, error) {
	if s == "" {
		return serviceHealthState{}, nil
	}
	if len(s) > serviceHealthMaxBytes {
		return serviceHealthState{}, errors.New("health state too large")
	}
	r := serviceHealthReader{quotaReader: quotaReader{b: []byte(s)}}
	v := r.state()
	return v, serviceWireDone(&r.quotaReader)
}
func encodeServiceHealthCommand(c serviceHealthCommand) (string, error) {
	w := serviceHealthWriter{b: []byte{c.Action}}
	w.params(c.Params)
	w.healthMap(c.Health)
	w.regionMap(c.Regions)
	w.str(c.Key)
	w.health(c.HealthRecord)
	w.region(c.RegionRecord)
	w.job(c.Job)
	w.flag(c.Flag)
	w.str(c.Text)
	w.str(c.Detail)
	w.n(uint64(c.Count))
	w.n(c.TimeToken)
	return w.finish()
}
func decodeServiceHealthCommand(s string) (serviceHealthCommand, error) {
	if s == "" || len(s) > serviceHealthMaxBytes {
		return serviceHealthCommand{}, errors.New("invalid health command size")
	}
	r := serviceHealthReader{quotaReader: quotaReader{b: []byte(s)}}
	v := serviceHealthCommand{Action: r.byte(), Params: r.params(), Health: r.healthMap(), Regions: r.regionMap(), Key: r.str(), HealthRecord: r.health(), RegionRecord: r.region(), Job: r.job(), Flag: r.flag(), Text: r.str(), Detail: r.str(), Count: r.platformInt(), TimeToken: r.n()}
	return v, serviceWireDone(&r.quotaReader)
}
func encodeServiceHealthQuery(q serviceHealthQuery) (string, error) {
	w := serviceHealthWriter{b: []byte{q.Kind}}
	w.str(q.Key)
	return w.finish()
}
func decodeServiceHealthQuery(s string) (serviceHealthQuery, error) {
	if s == "" || len(s) > serviceHealthMaxBytes {
		return serviceHealthQuery{}, errors.New("invalid health query size")
	}
	r := serviceHealthReader{quotaReader: quotaReader{b: []byte(s)}}
	v := serviceHealthQuery{Kind: r.byte(), Key: r.str()}
	return v, serviceWireDone(&r.quotaReader)
}
func encodeServiceHealthReply(v serviceHealthReply) (string, error) {
	w := serviceHealthWriter{}
	w.flag(v.Found)
	w.state(v.State)
	w.health(v.HealthRecord)
	w.region(v.RegionRecord)
	return w.finish()
}
func decodeServiceHealthReply(s string) (serviceHealthReply, error) {
	if s == "" || len(s) > serviceHealthMaxBytes {
		return serviceHealthReply{}, errors.New("invalid health reply size")
	}
	r := serviceHealthReader{quotaReader: quotaReader{b: []byte(s)}}
	v := serviceHealthReply{Found: r.flag(), State: r.state(), HealthRecord: r.health(), RegionRecord: r.region()}
	return v, serviceWireDone(&r.quotaReader)
}
