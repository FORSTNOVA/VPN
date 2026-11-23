package main

import (
	"errors"
	"reflect"
)

type diagnosticFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type diagnosticFlowStart func([]byte) (diagnosticFlowSession, error)
type diagnosticFrame struct {
	kind    byte
	a, b, c int64
	text    string
	indices []int64
}

func diagnosticStartInput(q diagnoseServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"diagnose": 0, "ensure": 1, "details": 2, "dns": 3, "stun": 4}[q.Op]
	if !ok {
		return nil, errors.New("invalid diagnostic op")
	}
	return []byte{op}, nil
}
func decodeDiagnosticFrame(b []byte) (diagnosticFrame, error) {
	r := quotaReader{b: b}
	f := diagnosticFrame{kind: r.byte(), a: r.integer(), b: r.integer(), c: r.integer(), text: r.str()}
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
		return f, errors.New("invalid diagnostic frame")
	}
	return f, nil
}

type diagnosticArena struct {
	q                  diagnoseServiceRequest
	s                  *diagnoseServiceState
	next               int64
	errs               map[int64]error
	report             diagnoseReport
	details            egressFacts
	dns                dnsFacts
	mapped             string
	elapsed            int64
	quality            diagnoseQualityInput
	selected           string
	temporary          bool
	lastError          error
	detailOK, stunOK   bool
	detailErr, stunErr error
}

func newDiagnosticArena(q diagnoseServiceRequest) *diagnosticArena {
	return &diagnosticArena{q: q, errs: map[int64]error{}}
}

var _ = reflect.DeepEqual

func (a *diagnosticArena) qualityProjection(p int64) ipQualityFacts {
	v := ipQualityFacts{FraudScore: int(p & 255), IsHosting: p&(1<<10) != 0, IsNative: p&(1<<11) != 0, IsProxy: p&(1<<12) != 0, IsVPN: p&(1<<13) != 0, IsTor: p&(1<<14) != 0}
	if p == 0 {
		v.IPType = diagnoseQualityUnknown()
		v.RiskLevel = diagnoseQualityUnknown()
		return v
	}
	v.IPType = diagnoseQualityResidential()
	if v.IsHosting {
		v.IPType = diagnoseQualityHosting()
	}
	switch (p >> 8) & 3 {
	case 1:
		v.RiskLevel = diagnoseQualityLow()
	case 2:
		v.RiskLevel = diagnoseQualityMedium()
	case 3:
		v.RiskLevel = diagnoseQualityHigh()
	}
	return v
}
func (a *diagnosticArena) frameResult(f diagnosticFrame) (diagnoseServiceResult, bool) {
	if len(f.indices) != 3 {
		return diagnoseServiceResult{}, false
	}
	r := diagnoseServiceResult{}
	err := a.errs[f.c]
	switch f.b {
	case 0:
	case 10:
		if err == nil {
			return r, false
		}
	case 11:
		err = errors.New(diagnoseNoDetailsMessage())
	case 12:
		err = errors.New(diagnoseSTUNBuildMessage())
	case 13:
		err = errors.New(diagnoseSTUNEmptyMessage())
	case 20:
	case 21:
	default:
		return r, false
	}
	switch a.q.Op {
	case "ensure":
		r.Temporary = f.indices[0] != 0
		r.Err = err
	case "details":
		r.Err = err
		if err == nil {
			r.Egress = a.details
			r.Quality = a.qualityProjection(f.indices[1])
		}
	case "dns":
		r.DNS = a.dns
	case "stun":
		r.Err = err
		if err == nil {
			r.Mapped = a.mapped
			r.Elapsed = a.elapsed
		}
	case "diagnose":
		r.Status = int(f.a)
		if f.b == 10 {
			r.Message = err.Error()
		} else if f.b == 20 {
			r.Message = "select a subscription node before checking"
		} else {
			r.Report = a.report
			if f.b == 21 {
				r.Report.Egress.Error = diagnoseExitFailureMessage()
				for i, n := range a.s.SiteNames {
					r.Report.Sites[i] = siteCheckResult{Name: n, State: "skipped", Detail: diagnoseSkippedMessage()}
				}
			} else {
				if a.detailOK {
					r.Report.Quality = a.qualityProjection(f.indices[1])
				}
				v := f.indices[2]
				r.Report.DNS.MatchesExit = v&1 != 0
				r.Report.UDP.MatchesExit = v&2 != 0
				r.Report.Quality.WebRTCLeak = v&4 != 0
				r.Report.Quality.DNSLeak = v&8 != 0
			}
		}
	default:
		return r, false
	}
	return r, true
}
func (a *diagnosticArena) port(f diagnosticFrame) (diagnoseEffect, bool) {
	e := diagnoseEffect{}
	switch f.kind {
	case 1:
		e.Kind = "lock"
	case 2:
		e.Kind = "running"
	case 3:
		e.Kind = "start"
	case 4:
		e.Kind = "snapshot"
		if f.a == 1 {
			e.Kind = "choice"
		}
	case 5, 7:
		e.Kind = "put"
		e.Name = f.text
	case 6:
		e.Kind = "list"
	case 8:
		e.Kind = "get"
		e.Path = "/proxies/" + f.text
	case 9:
		e.Kind = "ipv4"
	case 10:
		e.Kind = "trace4"
	case 11:
		e.Kind = "ipv6"
	case 12:
		e.Kind = "detail"
		e.Index = int(f.a)
		if a.s == nil || f.a < 0 || f.a >= int64(a.s.DetailsCount) {
			return e, false
		}
	case 13:
		e.Kind = "quality"
	case 14:
		e.Kind = "resolver"
		a.finishDetails()
	case 15:
		e.Kind = "country"
		e.Address = a.dns.Resolver
	case 16:
		e.Kind = "relay"
		a.report.DNS = a.dns
	case 17:
		e.Kind = "stun-build"
	case 18:
		e.Kind = "stun-exchange"
		e.Index = int(f.a)
		if a.s == nil || f.a < 0 || f.a >= int64(a.s.STUNCount) {
			return e, false
		}
	case 19:
		e.Kind = "sites-start"
		e.Indices = make([]int, len(f.indices))
		for i, v := range f.indices {
			e.Indices[i] = int(v)
		}
		if a.s == nil || len(f.indices) != len(a.s.SiteNames) {
			return e, false
		}
		for i, v := range f.indices {
			if v != int64(i) {
				return e, false
			}
		}
		a.finishStun()
	case 25:
		e.Kind = "sites-join"
	case 20:
		e.Kind = "verdict"
		e.Report = a.report
	case 21:
		e.Kind = "response"
		r, ok := a.frameResult(f)
		if !ok {
			return e, false
		}
		e.Status = r.Status
		e.Message = r.Message
		e.Report = r.Report
	case 22:
		e.Kind = "stop"
	case 23:
		e.Kind = "context"
	case 24:
		e.Kind = "unlock"
	default:
		return e, false
	}
	return e, true
}
func (a *diagnosticArena) finishDetails() {
	if a.q.Op != "diagnose" {
		return
	}
	if !a.detailOK {
		err := a.detailErr
		if err == nil {
			err = errors.New(diagnoseNoDetailsMessage())
		}
		a.report.Egress.Error = err.Error()
		return
	}
	v4, v6 := a.report.Egress.IPv4, a.report.Egress.IPv6
	a.report.Egress = a.details
	a.report.Egress.Error = ""
	if v4 != "" {
		a.report.Egress.IPv4 = v4
	}
	if v6 != "" {
		a.report.Egress.IPv6 = v6
	}
	a.report.Quality = diagnoseQualityFromFacts(a.quality)
}
func (a *diagnosticArena) finishStun() {
	if a.stunOK {
		a.report.UDP.Mapped = a.mapped
	} else {
		err := a.stunErr
		if err == nil {
			err = errors.New(diagnoseSTUNEmptyMessage())
		}
		a.report.UDP.Error = err.Error()
	}
}
func (a *diagnosticArena) replyBytes(f diagnosticFrame, r diagnoseReply) ([]byte, error) {
	if r.State != nil {
		a.s = cloneDiagnoseServiceState(r.State)
	}
	var value, token int64
	name := ""
	flag := r.Bool
	if r.Err != nil {
		a.next++
		token = a.next
		a.errs[token] = r.Err
	}
	switch f.kind {
	case 3:
		if r.Err == nil {
			a.temporary = true
		}
	case 4:
		flag = f.a == 1
	case 8:
		name, _ = r.Value.(string)
		if r.Err == nil {
			a.selected = name
		}
	case 9, 10:
		a.report.Egress.IPv4 = r.Address
		name = r.Address
	case 11:
		a.report.Egress.IPv6 = r.Address
	case 12:
		if r.Err == nil {
			a.details = r.Egress
			a.detailOK = true
		} else {
			a.detailErr = r.Err
		}
	case 13:
		a.quality = r.Quality
		name = r.Quality.Lower
		for i, v := range []bool{r.Quality.Valid, r.Quality.Hosting, r.Quality.Proxy, r.Quality.VPN, r.Quality.Tor, r.Quality.Anonymous} {
			if v {
				value |= 1 << i
			}
		}
	case 14:
		if r.Err != nil {
			a.dns = dnsFacts{Error: r.Err.Error()}
		} else {
			a.dns = r.DNS
		}
	case 15:
		if r.Err == nil {
			a.dns.Country = r.Address
		}
	case 16:
		a.report.UDP.Relay = r.Relay
	case 17:
		if r.Err != nil {
			a.stunErr = errors.New(diagnoseSTUNBuildMessage())
		}
	case 18:
		if r.Err == nil {
			a.mapped = r.Mapped
			a.elapsed = r.Elapsed
			a.stunOK = true
		} else {
			a.stunErr = r.Err
		}
	case 25:
		a.report.Sites = r.Sites
	case 20:
		for i, v := range []bool{a.report.DNS.Country != "", a.report.Egress.CountryCode != "", r.DNSMatch, a.report.UDP.Mapped != "", r.UDPMatch} {
			if v {
				value |= 1 << i
			}
		}
	case 23:
		a.report = diagnoseReport{Node: a.selected, Temporary: a.temporary, Sites: make([]siteCheckResult, len(a.s.SiteNames))}
	}
	b := registryBool([]byte{f.kind}, r.Err == nil)
	b = registryBool(b, flag)
	b = registryInt(b, value)
	b = registryInt(b, token)
	b = registryBytes(b, name)
	b = registryBytes(b, "")
	b = profileStrings(b, r.Names)
	b = registryBool(b, r.State != nil)
	if r.State != nil {
		b = registryBool(b, r.State.Chain)
		b = registryBytes(b, r.State.Choice)
		b = registryInt(b, int64(r.State.DetailsCount))
		b = registryInt(b, int64(r.State.STUNCount))
		b = registryInt(b, int64(len(r.State.SiteNames)))
	}
	return b, nil
}
func (a *diagnosticArena) resultMatches(f diagnosticFrame, r diagnoseServiceResult) bool {
	w, ok := a.frameResult(f)
	if !ok {
		return false
	}
	if (w.Err == nil) != (r.Err == nil) {
		return false
	}
	if f.b == 10 && w.Err != nil && !errors.Is(r.Err, w.Err) {
		return false
	}
	if w.Err != nil && w.Err.Error() != r.Err.Error() {
		return false
	}
	w.Err = nil
	r.Err = nil
	return reflect.DeepEqual(w, r)
}
