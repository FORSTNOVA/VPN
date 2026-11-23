package main

import (
	"errors"
	"reflect"
	"time"
)

type chainProbeFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type chainProbeFlowStart func([]byte) (chainProbeFlowSession, error)
type chainProbeFrame struct {
	kind    byte
	a, b, c int64
	text    string
	indices []int64
}

func chainProbeStartInput(q chainProbeServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"probe": 0, "list": 1, "measure-both": 2, "measure-prefix": 3, "timed-requests": 4}[q.Op]
	if !ok {
		return nil, errors.New("invalid chain probe op")
	}
	return []byte{op}, nil
}
func decodeChainProbeFrame(b []byte) (chainProbeFrame, error) {
	r := quotaReader{b: b}
	f := chainProbeFrame{kind: r.byte(), a: r.integer(), b: r.integer(), c: r.integer(), text: r.str()}
	n := r.integer()
	if n < 0 || n > 100000 || n > int64((len(b)-r.p)/8) {
		r.bad = true
	} else {
		f.indices = make([]int64, n)
		for i := range f.indices {
			f.indices[i] = r.integer()
		}
	}
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid chain probe frame")
	}
	return f, nil
}

type chainProbeArena struct {
	q                          chainProbeServiceRequest
	s                          *chainProbeState
	next                       int64
	errs                       map[int64]error
	started, requestStart      time.Time
	node                       chainProbeNodeFacts
	urls                       []string
	policy                     map[string][]string
	fingerprint, path, details string
	records                    []probeRecord
	result                     probeResult
	results                    []probeResult
	recordIndex                int
	now                        time.Time
	obs                        chainProbeReply
	recordResults              map[int]probeResult
}

func newChainProbeArena(q chainProbeServiceRequest) *chainProbeArena {
	return &chainProbeArena{q: q, errs: map[int64]error{}, urls: q.EntryURLs, started: q.Started, requestStart: q.Started, results: []probeResult{}, recordResults: map[int]probeResult{}}
}

var _ = reflect.DeepEqual

func (a *chainProbeArena) errorFor(f chainProbeFrame) error {
	switch f.c {
	case 0:
		return nil
	case 1:
		if len(f.indices) < 2 {
			return errors.New("invalid error token")
		}
		return a.errs[f.indices[1]]
	case 3, 4, 5, 6, 7:
		return errors.New(chainProbeError(int(f.c) - 3))
	case 8:
		if len(f.indices) < 5 {
			return errors.New("invalid status")
		}
		return errors.New(chainProbeStatusError(int(f.indices[4])))
	case 9:
		return errors.New(chainProbeError(5))
	case 10:
		return errors.New(chainProbeHTTP(1))
	case 11:
		return errors.New(chainProbeHTTP(2))
	case 12:
		return errors.New(chainProbeHTTP(4))
	case 13:
		return errors.New(chainProbeHTTP(5))
	}
	return errors.New("unknown probe failure")
}
func (a *chainProbeArena) port(f chainProbeFrame) (chainProbeEffect, bool) {
	e := chainProbeEffect{}
	switch f.kind {
	case 1:
		e.Kind = "lock"
	case 2:
		e.Kind = "unlock"
	case 3:
		e.Kind = "snapshot"
	case 4:
		e.Kind = "index"
	case 5:
		e.Kind = "mode"
	case 6:
		e.Kind = "policy"
	case 7:
		e.Kind = "compile"
		e.Hops = a.q.Hops
		if f.b == 1 {
			e.Hops = a.result.Hops
		}
	case 8:
		e.Kind = "fingerprint"
	case 9:
		e.Kind = "parent-context"
	case 10:
		e.Kind = "entry-policy"
	case 11:
		e.Kind = "now"
	case 12:
		e.Kind = "stage"
		i, ok := map[int64]int{0: 0, 1: 1, 2: 2, 3: 12, 4: 13, 5: 8}[f.a]
		if !ok {
			return e, false
		}
		e.Text = chainProbeStage(i)
	case 13:
		e.Kind = "node"
		e.Index = int(f.a)
	case 14:
		e.Kind = "resolver"
		e.Node = a.node
		e.URLs = a.urls
	case 15:
		e.Kind = "ech"
		e.Time = a.started.Add(12 * time.Second)
		switch f.a {
		case 0:
			e.Name = a.node.ECHQuery
		case 1:
			e.Name = a.node.ServerName
		case 2:
			e.Name = a.node.SNI
		case 3:
			e.Name = a.node.Server
		default:
			return e, false
		}
	case 16:
		e.Kind = "ech-apply"
	case 17:
		var opts int64
		for i, v := range []bool{a.node.Interface != "", a.node.RoutingMark != 0, a.node.TFO, a.node.MPTCP} {
			if v {
				opts |= 1 << i
			}
		}
		opts |= map[string]int64{"ipv4": 1, "ipv6": 2, "ipv4-prefer": 3, "ipv6-prefer": 4}[a.node.IPVersion] << 4
		if f.a != opts {
			return e, false
		}
		e.Kind = "entry-dialer"
		e.Node = a.node
	case 18:
		e.Kind = "adapter"
		e.Index = int(f.a)
	case 19:
		e.Kind = "transport"
	case 20:
		e.Kind = "request-new"
		e.Time = a.requestStart
		e.Target = a.q.Target
		e.Budget = a.q.Budget
		if a.q.Op != "timed-requests" {
			e.Budget = 12 * time.Second
		}
		if a.q.Op == "probe" {
			e.Target = probeTarget
		}
	case 21:
		e.Kind = "request-do"
	case 22:
		e.Kind = "elapsed"
		e.Time = a.requestStart
	case 23:
		e.Kind = "body-close"
	case 24:
		e.Kind = "request-cancel"
	case 25:
		e.Kind = "idle-close"
	case 26:
		e.Kind = "node-close"
		e.Index = int(f.a)
	case 27:
		e.Kind = "resolver-reset"
	case 28:
		e.Kind = "observation"
	case 29:
		e.Kind = "store-check"
	case 30:
		e.Kind = "path-encode"
		e.Hops = a.q.Hops
	case 31:
		e.Kind = "result-encode"
		a.projectProbe(f)
		e.Result = a.result
	case 32:
		e.Kind = "save"
		e.Report = probeRecord{Path: a.path, Fingerprint: a.fingerprint, Delay: a.result.Delay, Error: a.result.Error, Measured: a.result.Measured, Details: a.details}
	case 33:
		e.Kind = "records"
	case 34:
		if f.a < 0 || f.a >= int64(len(a.records)) {
			return e, false
		}
		a.recordIndex = int(f.a)
		r := a.records[f.a]
		a.result = probeResult{Delay: r.Delay, Error: r.Error, Measured: r.Measured, Target: probeTarget}
		e.Kind = "details-decode"
		e.Index = int(f.a)
		e.Result = a.result
	case 35:
		e.Kind = "path-decode"
		e.Index = int(f.a)
		e.Result = a.result
	case 36:
		r, ok := a.frameResult(f)
		if !ok {
			return e, false
		}
		e.Kind = "response"
		e.Status = r.Status
		e.Text = r.Message
		e.Result = r.Result
		e.Results = r.Results
	case 37:
		e.Kind = "parent-cancel"
	case 38:
		e.Kind = "entry-select"
		switch f.a {
		case 0:
		case 1:
			a.urls = []string{entryUDP1, entryUDP2}
		case 2:
			a.urls = []string{entryDoH}
		default:
			return e, false
		}
		e.URLs = a.urls
	default:
		return e, false
	}
	return e, true
}
func (a *chainProbeArena) replyBytes(f chainProbeFrame, r chainProbeReply) ([]byte, error) {
	if r.State != nil {
		v := cloneChainProbeState(r.State)
		if f.kind == 7 && a.s != nil {
			v.TUN = a.s.TUN
			v.StrictDNS = a.s.StrictDNS
		}
		a.s = v
	}
	var bits, value, token int64
	flag := r.Bool
	if r.Err != nil {
		a.next++
		token = a.next
		a.errs[token] = r.Err
	}
	switch f.kind {
	case 6:
		a.policy = r.Policy
	case 8:
		if a.q.Op == "list" {
			flag = a.records[a.recordIndex].Fingerprint == r.Text
		} else {
			a.fingerprint = r.Text
		}
	case 10:
		a.urls = r.URLs
		if len(r.URLs) > 0 {
			bits |= 1
		}
		if r.Index > 0 {
			bits |= 2
		}
	case 11:
		if f.a == 0 {
			a.started = r.Now
			a.requestStart = r.Now
		} else if f.a == 1 {
			a.requestStart = r.Now
		} else {
			a.now = r.Now
			a.result.Measured = r.Now.UnixMilli()
		}
	case 13:
		a.node = r.Node
		n := r.Node
		for i, v := range []bool{n.Interface != "", n.RoutingMark != 0, n.TFO, n.MPTCP} {
			if v {
				bits |= 1 << i
			}
		}
		bits |= map[string]int64{"ipv4": 1, "ipv6": 2, "ipv4-prefer": 3, "ipv6-prefer": 4}[n.IPVersion] << 4
		for i, v := range []bool{n.ECHPresent, n.ECHEnabled, n.ECHConfig != "", n.ECHQuery != "", n.ServerName != "", n.SNI != "", n.Server != ""} {
			if v {
				bits |= 1 << (i + 7)
			}
		}
	case 21:
		if r.Deadline {
			bits |= 1
		}
		if r.Reused {
			bits |= 2
		}
		value = int64(r.HTTPStatus)
	case 22:
		value = r.Elapsed
	case 28:
		a.obs = r
		a.projectProbe(f)
		if r.IP != "" {
			bits |= 1
		}
		if r.Attempt != "" {
			bits |= 2
		}
		if r.Family == "IPv4" {
			bits |= 4
		} else if r.Family == "IPv6" {
			bits |= 8
		}
	case 30:
		a.path = r.Text
	case 31:
		a.details = r.Text
	case 33:
		a.records = r.Records
		value = int64(len(r.Records))
	case 34, 35:
		a.result = r.Result
		if f.kind == 35 && r.Err == nil {
			a.recordResults[a.recordIndex] = r.Result
		}
	}
	b := registryBool([]byte{f.kind}, r.Err == nil)
	b = registryBool(b, flag)
	b = registryInt(b, bits)
	b = registryInt(b, value)
	b = registryInt(b, token)
	b = registryBytes(b, "")
	b = registryBool(b, r.State != nil)
	if r.State != nil {
		b = registryBool(b, r.State.Subscription)
		b = registryBool(b, a.s.TUN)
		b = registryBool(b, a.s.StrictDNS)
		b = registryBool(b, r.State.Store)
		b = registryInt(b, int64(r.State.ProxyCount))
		b = registryBool(b, r.State.HasURLs)
	}
	return b, nil
}
func (a *chainProbeArena) frameResult(f chainProbeFrame) (chainProbeServiceResult, bool) {
	if len(f.indices) < 5 {
		return chainProbeServiceResult{}, false
	}
	r := chainProbeServiceResult{}
	err := a.errorFor(f)
	switch a.q.Op {
	case "measure-both", "timed-requests":
		r.Cold = f.a
		r.Warm = f.b
		r.Err = err
	case "measure-prefix":
		r.Cold = f.a
		r.Warm = f.b
		r.Err = err
	case "probe":
		r.Status = int(f.indices[0])
		if r.Status != 200 {
			if err == nil {
				return r, false
			}
			r.Message = err.Error()
		} else {
			r.Result = a.result
		}
	case "list":
		r.Status = int(f.indices[0])
		if r.Status != 200 {
			if err == nil {
				return r, false
			}
			r.Message = err.Error()
		} else {
			r.Results = []probeResult{}
			for _, i := range f.indices[5:] {
				v, ok := a.recordResults[int(i)]
				if !ok {
					return r, false
				}
				r.Results = append(r.Results, v)
			}
		}
	default:
		return r, false
	}
	return r, true
}
func (a *chainProbeArena) resultMatches(f chainProbeFrame, r chainProbeServiceResult) bool {
	w, ok := a.frameResult(f)
	if !ok {
		return false
	}
	if (w.Err == nil) != (r.Err == nil) {
		return false
	}
	if w.Err != nil {
		if f.c == 1 && !errors.Is(r.Err, w.Err) {
			return false
		}
		if w.Err.Error() != r.Err.Error() {
			return false
		}
	}
	w.Err = nil
	r.Err = nil
	return reflect.DeepEqual(w, r)
}

func (a *chainProbeArena) projectProbe(f chainProbeFrame) {
	if len(f.indices) < 5 {
		return
	}
	a.result = probeResult{Hops: a.q.Hops, Target: probeTarget, DiagnosticsVersion: 1, Delay: f.a, WarmDelay: f.b, Measured: a.now.UnixMilli()}
	if f.kind == 28 {
		a.result.Measured = 0
		a.result.EntryIP = a.obs.IP
		if a.result.EntryIP == "" {
			a.result.EntryIP = a.obs.Attempt
			a.result.EntryAttempt = a.result.EntryIP != ""
		}
		a.result.EntryFamily = a.obs.Family
	} else {
		v := f.indices[2]
		if v&1 != 0 {
			a.result.EntryIP = a.obs.IP
		} else if v&2 != 0 {
			a.result.EntryIP = a.obs.Attempt
			a.result.EntryAttempt = true
		}
		if v&4 != 0 {
			a.result.EntryFamily = "IPv4"
		} else if v&8 != 0 {
			a.result.EntryFamily = "IPv6"
		}
	}
	if e := a.errorFor(f); e != nil {
		a.result.FailureStage = a.obs.Stage
		if f.a > 0 {
			a.result.WarmError = e.Error()
		} else {
			a.result.Error = e.Error()
		}
	}
}
