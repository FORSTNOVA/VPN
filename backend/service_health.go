package main

import (
	"errors"
	"math"
	"reflect"
	"time"
)

// Map keys are independent of record.Name. Time values live only in the Go
// immutable clock ledger; the domain owns the authoritative token references.
type serviceHealthRecord struct {
	Record                                        nodeHealth
	LastSuccessToken, CooldownToken, UpdatedToken uint64
}
type serviceRegionRecord struct {
	Record        regionRecord
	VerifiedToken uint64
}
type serviceHealthState struct {
	Params                 healthParams
	Health                 map[string]serviceHealthRecord
	Regions                map[string]serviceRegionRecord
	Job                    *regionJob
	Blocked                bool
	BlockReason, SweepNote string
	LastSwitchToken        uint64
}
type serviceHealthCommand struct {
	Action       uint8
	Params       healthParams
	Health       map[string]serviceHealthRecord
	Regions      map[string]serviceRegionRecord
	Key          string
	HealthRecord serviceHealthRecord
	RegionRecord serviceRegionRecord
	Job          *regionJob
	Flag         bool
	Text, Detail string
	Count        int
	TimeToken    uint64
}
type serviceHealthQuery struct {
	Kind uint8
	Key  string
}
type serviceHealthReply struct {
	Found        bool
	State        serviceHealthState
	HealthRecord serviceHealthRecord
	RegionRecord serviceRegionRecord
}

const (
	healthStateParams uint8 = 1 + iota
	healthStateRestoreHealth
	healthStateRestoreRegions
	healthStateRecordHealth
	healthStateRecordRegion
	healthStateClearHealth
	healthStateClearRegions
	healthStateBlocked
	healthStateBlockReason
	healthStateSweepNote
	healthStateLastSwitch
	healthStateCreateJob
	healthStateJobTotal
	healthStateJobAdvance
	healthStateJobFinish
)

// Queries: 0 metadata only, 1 health by key, 2 region by key, 3 full report.
type healthDomainProjection struct {
	Params                 healthParams
	Health                 map[string]nodeHealth
	Regions                map[string]regionRecord
	Job                    *regionJob
	Blocked                bool
	BlockReason, SweepNote string
	LastSwitch             time.Time
}

func applyServiceHealth(s *serviceHealthState, c serviceHealthCommand) error {
	switch c.Action {
	case healthStateParams:
		s.Params = c.Params
	case healthStateRestoreHealth:
		s.Health = c.Health
	case healthStateRestoreRegions:
		s.Regions = c.Regions
	case healthStateRecordHealth:
		if s.Health == nil {
			return errors.New("nil health map")
		}
		if _, ok := s.Health[c.Key]; !ok && len(s.Health) >= 100000 {
			return errors.New("health map limit")
		}
		s.Health[c.Key] = c.HealthRecord
	case healthStateRecordRegion:
		if s.Regions == nil {
			return errors.New("nil region map")
		}
		if _, ok := s.Regions[c.Key]; !ok && len(s.Regions) >= 100000 {
			return errors.New("region map limit")
		}
		s.Regions[c.Key] = c.RegionRecord
	case healthStateClearHealth:
		s.Health = map[string]serviceHealthRecord{}
	case healthStateClearRegions:
		s.Regions = map[string]serviceRegionRecord{}
	case healthStateBlocked:
		s.Blocked = c.Flag
	case healthStateBlockReason:
		s.BlockReason = c.Text
	case healthStateSweepNote:
		s.SweepNote = c.Text
	case healthStateLastSwitch:
		s.LastSwitchToken = c.TimeToken
	case healthStateCreateJob:
		if c.Job == nil {
			s.Job = nil
		} else {
			v := *c.Job
			s.Job = &v
		}
	case healthStateJobTotal:
		if s.Job == nil {
			return errors.New("nil region job")
		}
		s.Job.Total = c.Count
	case healthStateJobAdvance:
		if s.Job == nil {
			return errors.New("nil region job")
		}
		s.Job.Done++
		switch c.Text {
		case "skip":
			s.Job.Skipped++
		case "verified":
			s.Job.Verified++
		case "failed":
			s.Job.Failed++
		}
	case healthStateJobFinish:
		if s.Job != nil {
			s.Job.Running = false
			s.Job.FinishedAt = c.Text
			if c.Flag {
				s.Job.LastError = c.Detail
			}
		}
	default:
		return errors.New("invalid health action")
	}
	return nil
}
func serviceHealthMetadata(s serviceHealthState) serviceHealthState {
	s.Health = nil
	s.Regions = nil
	if s.Job != nil {
		j := *s.Job
		s.Job = &j
	}
	return s
}
func (b *goServiceStateBackend) HealthQuery(payload string) (string, error) {
	if b.closed {
		return "", errServiceStateClosed
	}
	q, e := decodeServiceHealthQuery(payload)
	if e != nil {
		return "", e
	}
	r := serviceHealthReply{}
	switch q.Kind {
	case 0:
		r.State = serviceHealthMetadata(b.health)
	case 1:
		r.HealthRecord, r.Found = b.health.Health[q.Key]
	case 2:
		r.RegionRecord, r.Found = b.health.Regions[q.Key]
	case 3:
		r.State = b.health
	default:
		return "", errors.New("invalid health query")
	}
	return encodeServiceHealthReply(r)
}
func (b *goServiceStateBackend) Meta() (serviceStateSnapshot, error) {
	if b.closed {
		return serviceStateSnapshot{}, errServiceStateClosed
	}
	return b.state.Snapshot, nil
}
func (s *serviceAuthority) healthQuery(q serviceHealthQuery) (serviceHealthReply, error) {
	if s == nil || s.closed {
		return serviceHealthReply{}, errServiceStateClosed
	}
	if s.catalogFault != nil {
		return serviceHealthReply{}, s.catalogFault
	}
	if s.pending != nil {
		return serviceHealthReply{}, errors.New("service command unresolved")
	}
	p, e := encodeServiceHealthQuery(q)
	if e != nil {
		return serviceHealthReply{}, e
	}
	v, e := s.backend.HealthQuery(p)
	if e != nil {
		return serviceHealthReply{}, e
	}
	return decodeServiceHealthReply(v)
}
func (s *serviceAuthority) healthCommand(c serviceHealthCommand, retire []uint64) error {
	confirmed := false
	defer func() {
		if confirmed {
			s.pendingHealthClocks = nil
		} else if s.pending == nil {
			for t := range s.pendingHealthClocks {
				delete(s.clocks, t)
				delete(s.healthClocks, t)
			}
			s.pendingHealthClocks = nil
		}
	}()
	p, e := encodeServiceHealthCommand(c)
	if e != nil {
		return e
	}
	if e = s.mutate(serviceStateCommand{Kind: 4, HealthPayload: p}); e != nil {
		return e
	}
	confirmed = true
	for _, t := range retire {
		delete(s.healthClocks, t)
		delete(s.clocks, t)
	}
	return nil
}
func (s *serviceAuthority) healthToken(v time.Time) uint64 {
	if v.IsZero() {
		return 0
	}
	if s.nextToken == math.MaxUint64 {
		panic("health clock exhausted")
	}
	s.nextToken++
	if s.healthClocks == nil {
		s.healthClocks = map[uint64]bool{}
	}
	s.healthClocks[s.nextToken] = true
	if s.pendingHealthClocks == nil {
		s.pendingHealthClocks = map[uint64]bool{}
	}
	s.pendingHealthClocks[s.nextToken] = true
	s.clocks[s.nextToken] = v
	return s.nextToken
}
func (s *serviceAuthority) healthTime(t uint64) time.Time {
	if t == 0 {
		return time.Time{}
	}
	v, ok := s.clocks[t]
	if !ok {
		panic("health clock missing")
	}
	return v
}
func (s *serviceAuthority) packHealth(v nodeHealth) serviceHealthRecord {
	r := serviceHealthRecord{Record: v, LastSuccessToken: s.healthToken(v.LastSuccessAt), CooldownToken: s.healthToken(v.CooldownUntil), UpdatedToken: s.healthToken(v.UpdatedAt)}
	r.Record.LastSuccessAt = time.Time{}
	r.Record.CooldownUntil = time.Time{}
	r.Record.UpdatedAt = time.Time{}
	return r
}
func (s *serviceAuthority) unpackHealth(r serviceHealthRecord) nodeHealth {
	v := r.Record
	v.LastSuccessAt = s.healthTime(r.LastSuccessToken)
	v.CooldownUntil = s.healthTime(r.CooldownToken)
	v.UpdatedAt = s.healthTime(r.UpdatedToken)
	return v
}
func (s *serviceAuthority) packRegion(v regionRecord) serviceRegionRecord {
	r := serviceRegionRecord{Record: v, VerifiedToken: s.healthToken(v.VerifiedAt)}
	r.Record.VerifiedAt = time.Time{}
	r.Record.Sources = cloneServiceStrings(v.Sources)
	return r
}
func (s *serviceAuthority) unpackRegion(r serviceRegionRecord) regionRecord {
	v := r.Record
	v.VerifiedAt = s.healthTime(r.VerifiedToken)
	v.Sources = cloneServiceStrings(v.Sources)
	return v
}
func healthRecordTokens(v serviceHealthRecord) []uint64 {
	return []uint64{v.LastSuccessToken, v.CooldownToken, v.UpdatedToken}
}
func (a *app) healthQueryLocked(q serviceHealthQuery) serviceHealthReply {
	r, e := a.serviceStateLocked().healthQuery(q)
	if e != nil {
		panic(e)
	}
	return r
}
func (a *app) healthMetaLocked() healthDomainProjection {
	return a.projectHealth(a.healthQueryLocked(serviceHealthQuery{}).State)
}
func (a *app) healthDomainLocked() healthDomainProjection {
	return a.projectHealth(a.healthQueryLocked(serviceHealthQuery{Kind: 3}).State)
}
func (a *app) projectHealth(v serviceHealthState) healthDomainProjection {
	s := a.serviceStateLocked()
	p := healthDomainProjection{Params: v.Params, Job: v.Job, Blocked: v.Blocked, BlockReason: v.BlockReason, SweepNote: v.SweepNote, LastSwitch: s.healthTime(v.LastSwitchToken)}
	if v.Health != nil {
		p.Health = make(map[string]nodeHealth, len(v.Health))
		for k, r := range v.Health {
			p.Health[k] = s.unpackHealth(r)
		}
	}
	if v.Regions != nil {
		p.Regions = make(map[string]regionRecord, len(v.Regions))
		for k, r := range v.Regions {
			p.Regions[k] = s.unpackRegion(r)
		}
	}
	return p
}
func (a *app) healthRecordLocked(k string) (nodeHealth, bool) {
	r := a.healthQueryLocked(serviceHealthQuery{Kind: 1, Key: k})
	return a.serviceStateLocked().unpackHealth(r.HealthRecord), r.Found
}
func (a *app) regionRecordLocked(k string) (regionRecord, bool) {
	r := a.healthQueryLocked(serviceHealthQuery{Kind: 2, Key: k})
	return a.serviceStateLocked().unpackRegion(r.RegionRecord), r.Found
}
func (a *app) applyHealthCommandLocked(c serviceHealthCommand, retire []uint64) {
	if e := a.serviceStateLocked().healthCommand(c, retire); e != nil {
		panic(e)
	}
}
func (a *app) replaceHealthParamsLocked(v healthParams) {
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateParams, Params: v}, nil)
}
func (a *app) writeHealthRecordLocked(k string, v nodeHealth) {
	s := a.serviceStateLocked()
	old := a.healthQueryLocked(serviceHealthQuery{Kind: 1, Key: k})
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateRecordHealth, Key: k, HealthRecord: s.packHealth(v)}, healthRecordTokens(old.HealthRecord))
}
func (a *app) writeRegionRecordLocked(k string, v regionRecord) {
	s := a.serviceStateLocked()
	old := a.healthQueryLocked(serviceHealthQuery{Kind: 2, Key: k})
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateRecordRegion, Key: k, RegionRecord: s.packRegion(v)}, []uint64{old.RegionRecord.VerifiedToken})
}
func (a *app) restoreHealthRecordsLocked(v map[string]nodeHealth) {
	s := a.serviceStateLocked()
	old := a.healthQueryLocked(serviceHealthQuery{Kind: 3}).State
	retire := []uint64{}
	for _, r := range old.Health {
		retire = append(retire, healthRecordTokens(r)...)
	}
	var out map[string]serviceHealthRecord
	if v != nil {
		out = make(map[string]serviceHealthRecord, len(v))
		for k, r := range v {
			out[k] = s.packHealth(r)
		}
	}
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateRestoreHealth, Health: out}, retire)
}
func (a *app) restoreRegionRecordsLocked(v map[string]regionRecord) {
	s := a.serviceStateLocked()
	old := a.healthQueryLocked(serviceHealthQuery{Kind: 3}).State
	retire := []uint64{}
	for _, r := range old.Regions {
		retire = append(retire, r.VerifiedToken)
	}
	var out map[string]serviceRegionRecord
	if v != nil {
		out = make(map[string]serviceRegionRecord, len(v))
		for k, r := range v {
			out[k] = s.packRegion(r)
		}
	}
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateRestoreRegions, Regions: out}, retire)
}
func (a *app) setHealthBlockedLocked(v bool) {
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateBlocked, Flag: v}, nil)
}
func (a *app) setHealthReasonLocked(v string) {
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateBlockReason, Text: v}, nil)
}
func (a *app) setHealthNoteLocked(v string) {
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateSweepNote, Text: v}, nil)
}
func (a *app) setHealthLastSwitchLocked(v time.Time) {
	s := a.serviceStateLocked()
	old := a.healthQueryLocked(serviceHealthQuery{}).State.LastSwitchToken
	a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateLastSwitch, TimeToken: s.healthToken(v)}, []uint64{old})
}
func (a *app) clearHealthObservationsLocked() {
	a.restoreHealthRecordsLocked(map[string]nodeHealth{})
	a.restoreRegionRecordsLocked(map[string]regionRecord{})
	a.setHealthBlockedLocked(false)
	a.setHealthReasonLocked("")
}

func (a *app) healthValueLocked(k string) nodeHealth   { v, _ := a.healthRecordLocked(k); return v }
func (a *app) regionValueLocked(k string) regionRecord { v, _ := a.regionRecordLocked(k); return v }

// Encoded-size accounting is a resource budget, not a second business state.
// Ordinary record updates inspect only the old/new entry; bulk restore measures
// its replacement once. Validate before any live map/job mutation.
func healthMapSize(v map[string]serviceHealthRecord) (int, error) {
	w := serviceHealthWriter{}
	w.healthMap(v)
	b, e := w.finish()
	return len(b), e
}
func regionMapSize(v map[string]serviceRegionRecord) (int, error) {
	w := serviceHealthWriter{}
	w.regionMap(v)
	b, e := w.finish()
	return len(b), e
}
func healthEntrySize(k string, v serviceHealthRecord) (int, error) {
	w := serviceHealthWriter{}
	w.str(k)
	w.health(v)
	b, e := w.finish()
	return len(b), e
}
func regionEntrySize(k string, v serviceRegionRecord) (int, error) {
	w := serviceHealthWriter{}
	w.str(k)
	w.region(v)
	b, e := w.finish()
	return len(b), e
}
func (b *goServiceStateBackend) applyHealth(c serviceHealthCommand) error {
	if b.healthMetaBytes == 0 {
		v, e := encodeServiceHealthState(serviceHealthMetadata(b.health))
		if e != nil {
			return e
		}
		b.healthMetaBytes = len(v)
		b.healthMapBytes, e = healthMapSize(b.health.Health)
		if e != nil {
			return e
		}
		b.regionMapBytes, e = regionMapSize(b.health.Regions)
		if e != nil {
			return e
		}
	}
	m, h, r := b.healthMetaBytes, b.healthMapBytes, b.regionMapBytes
	var err error
	switch c.Action {
	case healthStateRestoreHealth:
		h, err = healthMapSize(c.Health)
	case healthStateRestoreRegions:
		r, err = regionMapSize(c.Regions)
	case healthStateClearHealth:
		h = 9
	case healthStateClearRegions:
		r = 9
	case healthStateRecordHealth:
		if b.health.Health == nil {
			return errors.New("nil health map")
		}
		old, found := b.health.Health[c.Key]
		if !found && len(b.health.Health) >= serviceHealthMaxItems {
			return errors.New("health map limit")
		}
		n, e := healthEntrySize(c.Key, c.HealthRecord)
		if e != nil {
			return e
		}
		h += n
		if found {
			o, e := healthEntrySize(c.Key, old)
			if e != nil {
				return e
			}
			h -= o
		}
	case healthStateRecordRegion:
		if b.health.Regions == nil {
			return errors.New("nil region map")
		}
		old, found := b.health.Regions[c.Key]
		if !found && len(b.health.Regions) >= serviceHealthMaxItems {
			return errors.New("region map limit")
		}
		n, e := regionEntrySize(c.Key, c.RegionRecord)
		if e != nil {
			return e
		}
		r += n
		if found {
			o, e := regionEntrySize(c.Key, old)
			if e != nil {
				return e
			}
			r -= o
		}
	default:
		meta := serviceHealthMetadata(b.health)
		if e := applyServiceHealth(&meta, c); e != nil {
			return e
		}
		v, e := encodeServiceHealthState(meta)
		if e != nil {
			return e
		}
		m = len(v)
	}
	if err != nil {
		return err
	}
	if m+h+r-18 > serviceHealthMaxBytes-1024 {
		return errors.New("health state budget")
	}
	if err = applyServiceHealth(&b.health, c); err != nil {
		return err
	}
	b.healthMetaBytes, b.healthMapBytes, b.regionMapBytes = m, h, r
	return nil
}

func serviceHealthClockRefs(h serviceHealthState) map[uint64]bool {
	out := map[uint64]bool{}
	add := func(v uint64) {
		if v != 0 {
			out[v] = true
		}
	}
	add(h.LastSwitchToken)
	for _, v := range h.Health {
		add(v.LastSuccessToken)
		add(v.CooldownToken)
		add(v.UpdatedToken)
	}
	for _, v := range h.Regions {
		add(v.VerifiedToken)
	}
	return out
}
func validateServiceHealthLast(s serviceHealthState, c serviceHealthCommand) error {
	ok := false
	switch c.Action {
	case healthStateParams:
		ok = s.Params == c.Params
	case healthStateRestoreHealth:
		ok = reflect.DeepEqual(s.Health, c.Health)
	case healthStateRestoreRegions:
		ok = reflect.DeepEqual(s.Regions, c.Regions)
	case healthStateRecordHealth:
		v, found := s.Health[c.Key]
		ok = found && reflect.DeepEqual(v, c.HealthRecord)
	case healthStateRecordRegion:
		v, found := s.Regions[c.Key]
		ok = found && reflect.DeepEqual(v, c.RegionRecord)
	case healthStateClearHealth:
		ok = s.Health != nil && len(s.Health) == 0
	case healthStateClearRegions:
		ok = s.Regions != nil && len(s.Regions) == 0
	case healthStateBlocked:
		ok = s.Blocked == c.Flag
	case healthStateBlockReason:
		ok = s.BlockReason == c.Text
	case healthStateSweepNote:
		ok = s.SweepNote == c.Text
	case healthStateLastSwitch:
		ok = s.LastSwitchToken == c.TimeToken
	case healthStateCreateJob:
		ok = reflect.DeepEqual(s.Job, c.Job)
	case healthStateJobTotal:
		ok = s.Job != nil && s.Job.Total == c.Count
	case healthStateJobAdvance:
		ok = s.Job != nil // Previous wrapping counters unavailable in a single checkpoint.
	case healthStateJobFinish:
		ok = s.Job == nil || (!s.Job.Running && s.Job.FinishedAt == c.Text && (!c.Flag || s.Job.LastError == c.Detail))
	}
	if !ok {
		return errors.New("health checkpoint command mismatch")
	}
	return nil
}

// Controlled takeover only accepts the unique clock ownership produced by the
// application packers. Low-level typed domain values may otherwise share tokens.
func uniqueServiceHealthClockRefs(h serviceHealthState, live serviceStateSnapshot, p servicePathState) (map[uint64]bool, error) {
	refs := map[uint64]bool{}
	other := map[uint64]bool{}
	if live.HasBaseline {
		other[live.TimeToken] = true
	}
	if p.AtToken != 0 {
		other[p.AtToken] = true
	}
	for _, v := range p.Throttle {
		other[v.TimeToken] = true
	}
	add := func(t uint64) error {
		if t == 0 {
			return nil
		}
		if refs[t] || other[t] {
			return errors.New("health checkpoint clock ownership overlap")
		}
		refs[t] = true
		return nil
	}
	if e := add(h.LastSwitchToken); e != nil {
		return nil, e
	}
	for _, v := range h.Health {
		for _, t := range []uint64{v.LastSuccessToken, v.CooldownToken, v.UpdatedToken} {
			if e := add(t); e != nil {
				return nil, e
			}
		}
	}
	for _, v := range h.Regions {
		if e := add(v.VerifiedToken); e != nil {
			return nil, e
		}
	}
	return refs, nil
}
