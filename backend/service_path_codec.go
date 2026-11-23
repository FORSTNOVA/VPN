package main

import "errors"

const servicePathMaxBytes = 8 << 20
const servicePathMaxItems = 100000

type servicePathWriter struct {
	b   []byte
	bad bool
}

func (w *servicePathWriter) n(v uint64) {
	if w.bad {
		return
	}
	if len(w.b) > servicePathMaxBytes-8 {
		w.bad = true
		return
	}
	w.b = registryInt(w.b, int64(v))
}
func (w *servicePathWriter) flag(v bool) {
	if len(w.b) >= servicePathMaxBytes {
		w.bad = true
		return
	}
	w.b = registryBool(w.b, v)
}
func (w *servicePathWriter) str(v string) {
	if len(v) > servicePathMaxBytes || len(w.b) > servicePathMaxBytes-8-len(v) {
		w.bad = true
		return
	}
	w.n(uint64(len(v)))
	if !w.bad {
		w.b = append(w.b, v...)
	}
}
func (w *servicePathWriter) count(present bool, n int) {
	if n > servicePathMaxItems {
		w.bad = true
		return
	}
	w.flag(present)
	w.n(uint64(n))
}
func (w *servicePathWriter) strings(v []string) {
	w.count(v != nil, len(v))
	for _, x := range v {
		w.str(x)
	}
}
func (w *servicePathWriter) config(v pathCheckSettings) {
	w.flag(v.Enabled)
	w.n(uint64(v.IntervalSec))
	w.count(v.Targets != nil, len(v.Targets))
	for _, t := range v.Targets {
		w.str(t.Label)
		w.str(t.URL)
		w.flag(t.Builtin)
		w.flag(t.Enabled)
	}
}
func (w *servicePathWriter) results(v []siteCheckResult) {
	w.count(v != nil, len(v))
	for _, r := range v {
		w.str(r.Name)
		w.str(r.State)
		w.n(uint64(r.HTTPCode))
		w.n(uint64(r.LatencyMs))
		w.str(r.Detail)
	}
}
func (w *servicePathWriter) samples(v []servicePathSample) {
	w.count(v != nil, len(v))
	for _, x := range v {
		w.str(x.ID)
		w.n(uint64(x.Upload))
	}
}
func (w *servicePathWriter) finish() (string, error) {
	if w.bad {
		return "", errors.New("path state encoding limit")
	}
	return string(w.b), nil
}

type servicePathReader struct{ quotaReader }

func (r *servicePathReader) flag() bool { return serviceReadBool(&r.quotaReader) }
func (r *servicePathReader) n() uint64  { return uint64(r.integer()) }
func (r *servicePathReader) integerPlatform() int {
	n := r.integer()
	v := int(n)
	if int64(v) != n {
		r.bad = true
	}
	return v
}
func (r *servicePathReader) count() (bool, int) {
	p := r.flag()
	n := r.n()
	if n > servicePathMaxItems || (!p && n != 0) {
		r.bad = true
		return false, 0
	}
	return p, int(n)
}
func (r *servicePathReader) strings() []string {
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
func (r *servicePathReader) config() pathCheckSettings {
	v := pathCheckSettings{Enabled: r.flag(), IntervalSec: r.integerPlatform()}
	p, n := r.count()
	if p {
		v.Targets = make([]pathTarget, n)
		for i := range v.Targets {
			v.Targets[i] = pathTarget{Label: r.str(), URL: r.str(), Builtin: r.flag(), Enabled: r.flag()}
		}
	}
	return v
}
func (r *servicePathReader) results() []siteCheckResult {
	p, n := r.count()
	if !p {
		return nil
	}
	v := make([]siteCheckResult, n)
	for i := range v {
		v[i] = siteCheckResult{Name: r.str(), State: r.str(), HTTPCode: r.integerPlatform(), LatencyMs: r.integer(), Detail: r.str()}
	}
	return v
}
func (r *servicePathReader) samples() []servicePathSample {
	p, n := r.count()
	if !p {
		return nil
	}
	v := make([]servicePathSample, n)
	for i := range v {
		v[i] = servicePathSample{ID: r.str(), Upload: r.integer()}
	}
	return v
}
func encodeServicePathState(v servicePathState) (string, error) {
	if e := validateServicePathState(v); e != nil {
		return "", e
	}
	if !v.Status.At.IsZero() {
		return "", errors.New("path clock must be tokenized")
	}
	w := servicePathWriter{}
	w.config(v.Config)
	w.flag(v.Busy)
	w.flag(v.MonitorActive)
	s := v.Status
	w.flag(s.Running)
	w.str(s.ActiveNode)
	w.results(s.Results)
	w.strings(s.Failures)
	w.strings(s.Switched)
	w.str(s.Note)
	w.n(s.AlertID)
	w.str(s.Alert)
	w.str(s.RetryAt)
	w.str(s.RetryMode)
	w.flag(s.Demanded)
	w.str(s.Phase)
	w.n(v.AtToken)
	w.n(v.Generation)
	w.n(v.NextLease)
	w.n(v.LeaseID)
	w.samples(v.Previous)
	w.count(v.Throttle != nil, len(v.Throttle))
	for _, x := range v.Throttle {
		w.str(x.Label)
		w.n(x.TimeToken)
	}
	return w.finish()
}
func decodeServicePathState(b string) (servicePathState, error) {
	var v servicePathState
	if len(b) == 0 {
		return v, nil
	}
	if len(b) > servicePathMaxBytes {
		return v, errors.New("path state decoding limit")
	}
	r := servicePathReader{quotaReader: quotaReader{b: []byte(b)}}
	v.Config = r.config()
	v.Busy = r.flag()
	v.MonitorActive = r.flag()
	s := &v.Status
	s.Running = r.flag()
	s.ActiveNode = r.str()
	s.Results = r.results()
	s.Failures = r.strings()
	s.Switched = r.strings()
	s.Note = r.str()
	s.AlertID = r.n()
	s.Alert = r.str()
	s.RetryAt = r.str()
	s.RetryMode = r.str()
	s.Demanded = r.flag()
	s.Phase = r.str()
	v.AtToken = r.n()
	v.Generation = r.n()
	v.NextLease = r.n()
	v.LeaseID = r.n()
	v.Previous = r.samples()
	p, n := r.count()
	if p {
		v.Throttle = make([]servicePathThrottle, n)
		for i := range v.Throttle {
			v.Throttle[i] = servicePathThrottle{Label: r.str(), TimeToken: r.n()}
		}
	}
	if e := serviceWireDone(&r.quotaReader); e != nil {
		return v, e
	}
	return v, validateServicePathState(v)
}
func encodeServicePathCommand(v servicePathCommand) (string, error) {
	w := servicePathWriter{b: []byte{v.Action}}
	w.config(v.Config)
	w.flag(v.Flag)
	w.str(v.Text)
	w.n(v.TimeToken)
	w.n(v.Generation)
	w.n(v.LeaseID)
	w.strings(v.Strings)
	w.results(v.Results)
	w.samples(v.Samples)
	return w.finish()
}
func decodeServicePathCommand(b string) (servicePathCommand, error) {
	var v servicePathCommand
	if len(b) == 0 || len(b) > servicePathMaxBytes {
		return v, errors.New("path command decoding limit")
	}
	r := servicePathReader{quotaReader: quotaReader{b: []byte(b)}}
	v.Action = r.byte()
	v.Config = r.config()
	v.Flag = r.flag()
	v.Text = r.str()
	v.TimeToken = r.n()
	v.Generation = r.n()
	v.LeaseID = r.n()
	v.Strings = r.strings()
	v.Results = r.results()
	v.Samples = r.samples()
	return v, serviceWireDone(&r.quotaReader)
}
