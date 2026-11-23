package main

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync/atomic"
	"time"
)

type serviceStateSnapshot struct {
	Epoch, Revision uint64
	HasBaseline     bool
	TimeToken       uint64
	Up, Down        int64
	SamplingError   string
	PathPayload     string
	HealthPayload   string
	ControlPayload  string
	CatalogPayload  string
	ChinaPayload    string
}
type serviceStateCommand struct {
	Epoch, ID, ExpectedRevision uint64
	Kind                        uint8
	HasBaseline                 bool
	TimeToken                   uint64
	Up, Down                    int64
	SamplingError               string
	PathPayload                 string
	HealthPayload               string
	ControlPayload              string
	CatalogPayload              string
	ChinaPayload                string
}
type serviceStateAck struct{ Epoch, ID, Revision uint64 }
type serviceStateCheckpoint struct {
	Snapshot         serviceStateSnapshot
	LastCommand      serviceStateCommand
	LastAck          serviceStateAck
	HasLast, Pending bool
}
type serviceStateCommandStatus struct {
	Command        serviceStateCommand
	Ack            serviceStateAck
	Found, Pending bool
}
type serviceStateBackend interface {
	Read() (serviceStateSnapshot, error)
	Meta() (serviceStateSnapshot, error)
	HealthQuery(string) (string, error)
	ControlQuery() (string, error)
	ControlMeta() (string, error)
	CatalogQuery(string) (string, error)
	ChinaQuery() (string, error)
	Apply(serviceStateCommand) (serviceStateAck, error)
	CommandStatus(serviceStateCommand) (serviceStateCommandStatus, error)
	Acknowledge(serviceStateAck) error
	Export() (serviceStateCheckpoint, error)
	Rate(float64, int64, int64) (int64, int64, bool, error)
	Close() error
}

var serviceEpoch atomic.Uint64
var errServiceStateClosed = errors.New("service state closed")

type goServiceStateBackend struct {
	state                                           serviceStateCheckpoint
	health                                          serviceHealthState
	control                                         serviceControlState
	catalog                                         serviceCatalogState
	catalogRefs                                     map[uint64]uint64
	catalogIDs, catalogURLs                         map[string]int
	catalogBytes                                    int
	healthMetaBytes, healthMapBytes, regionMapBytes int
	closed                                          bool
}

func newGoServiceStateBackend() serviceStateBackend {
	payload, err := encodeServicePathState(servicePathState{})
	if err != nil {
		panic(err)
	}
	china, _ := encodeServiceChinaState(serviceChinaState{})
	return &goServiceStateBackend{state: serviceStateCheckpoint{Snapshot: serviceStateSnapshot{Epoch: serviceEpoch.Add(1), PathPayload: payload, ChinaPayload: china}}}
}
func (b *goServiceStateBackend) Read() (serviceStateSnapshot, error) {
	if b.closed {
		return serviceStateSnapshot{}, errServiceStateClosed
	}
	v := b.state.Snapshot
	var e error
	v.HealthPayload, e = encodeServiceHealthState(b.health)
	if e == nil {
		v.ControlPayload, e = encodeServiceControlState(b.control)
		if e == nil {
			v.CatalogPayload, e = encodeServiceCatalogState(b.catalog)
		}
	}
	return v, e
}
func (b *goServiceStateBackend) CommandStatus(c serviceStateCommand) (serviceStateCommandStatus, error) {
	if b.closed {
		return serviceStateCommandStatus{}, errServiceStateClosed
	}
	if c.Epoch != b.state.Snapshot.Epoch {
		return serviceStateCommandStatus{}, errors.New("service epoch mismatch")
	}
	if b.state.HasLast && c.ID == b.state.LastCommand.ID {
		if c != b.state.LastCommand {
			return serviceStateCommandStatus{}, errors.New("service command payload mismatch")
		}
		return serviceStateCommandStatus{b.state.LastCommand, b.state.LastAck, true, b.state.Pending}, nil
	}
	return serviceStateCommandStatus{}, nil
}
func (b *goServiceStateBackend) Acknowledge(a serviceStateAck) error {
	if b.closed {
		return errServiceStateClosed
	}
	if !b.state.HasLast || b.state.LastAck != a {
		return errors.New("service ack mismatch")
	}
	b.state.Pending = false
	return nil
}
func (b *goServiceStateBackend) Apply(c serviceStateCommand) (serviceStateAck, error) {
	status, err := b.CommandStatus(c)
	a := status.Ack
	if err != nil || status.Found {
		return a, err
	}
	if b.state.Pending {
		return a, errors.New("service command pending")
	}
	if len(c.SamplingError) > 1<<20 {
		return a, errors.New("service error too large")
	}
	s := b.state.Snapshot
	if c.Epoch == 0 || (c.Kind == 1 && c.HasBaseline != (c.TimeToken != 0)) || c.ID == 0 || (b.state.HasLast && c.ID <= b.state.LastCommand.ID) || c.ExpectedRevision != s.Revision || s.Revision == math.MaxUint64 {
		return a, errors.New("invalid service command revision")
	}
	switch c.Kind {
	case 7:
		v, e := decodeServiceChinaState(s.ChinaPayload)
		if e != nil {
			return a, e
		}
		cc, e := decodeServiceChinaCommand(c.ChinaPayload)
		if e != nil {
			return a, e
		}
		if e = applyServiceChina(&v, cc); e != nil {
			return a, e
		}
		s.ChinaPayload, e = encodeServiceChinaState(v)
		if e != nil {
			return a, e
		}
	case 1:
		s.HasBaseline = c.HasBaseline
		s.TimeToken = c.TimeToken
		s.Up = c.Up
		s.Down = c.Down
	case 2:
		s.SamplingError = c.SamplingError
	case 6:
		cc, e := decodeServiceCatalogCommand(c.CatalogPayload)
		if e != nil {
			return a, e
		}
		if e = b.applyCatalog(cc); e != nil {
			return a, e
		}
	case 5:
		cc, e := decodeServiceControlCommand(c.ControlPayload)
		if e != nil {
			return a, e
		}
		if e = applyServiceControl(&b.control, cc); e != nil {
			return a, e
		}
	case 4:
		c, e := decodeServiceHealthCommand(c.HealthPayload)
		if e != nil {
			return a, e
		}
		if e = b.applyHealth(c); e != nil {
			return a, e
		}
	case 3:
		p, e := decodeServicePathState(s.PathPayload)
		if e != nil {
			return a, e
		}
		q, e := decodeServicePathCommand(c.PathPayload)
		if e != nil {
			return a, e
		}
		p, e = applyServicePath(p, q)
		if e != nil {
			return a, e
		}
		s.PathPayload, e = encodeServicePathState(p)
		if e != nil {
			return a, e
		}
	default:
		return a, errors.New("invalid service command kind")
	}
	if s.HasBaseline && s.TimeToken == 0 {
		return a, errors.New("missing service clock token")
	}
	s.Revision++
	a = serviceStateAck{s.Epoch, c.ID, s.Revision}
	b.state = serviceStateCheckpoint{s, c, a, true, true}
	return a, nil
}
func (b *goServiceStateBackend) Export() (serviceStateCheckpoint, error) {
	if b.closed {
		return serviceStateCheckpoint{}, errServiceStateClosed
	}
	cp := b.state
	var e error
	cp.Snapshot.HealthPayload, e = encodeServiceHealthState(b.health)
	if e == nil {
		cp.Snapshot.ControlPayload, e = encodeServiceControlState(b.control)
		if e == nil {
			cp.Snapshot.CatalogPayload, e = encodeServiceCatalogState(b.catalog)
		}
	}
	return cp, e
}
func (b *goServiceStateBackend) Rate(seconds float64, up, down int64) (int64, int64, bool, error) {
	s, e := b.Meta()
	if e != nil {
		return 0, 0, false, e
	}
	if !s.HasBaseline || seconds <= 0 || up < s.Up || down < s.Down {
		return 0, 0, true, nil
	}
	u, d := float64(up-s.Up)/seconds, float64(down-s.Down)/seconds
	if math.IsNaN(u) || math.IsNaN(d) || math.IsInf(u, 0) || math.IsInf(d, 0) || u < -9223372036854775808.0 || u >= 9223372036854775808.0 || d < -9223372036854775808.0 || d >= 9223372036854775808.0 {
		return 0, 0, false, nil
	}
	return int64(u), int64(d), true, nil
}
func (b *goServiceStateBackend) Close() error { b.closed = true; return nil }

type serviceAuthority struct {
	backend                serviceStateBackend
	clocks                 map[uint64]time.Time
	healthClocks           map[uint64]bool
	otherClocks            map[uint64]bool
	pendingHealthClocks    map[uint64]bool
	nextToken, nextCommand uint64
	pending                *serviceStateCommand
	closed                 bool
	quotaStore             quotaCommands
	infos                  map[uint64]*subscriptionUserInfo
	infoReverse            map[*subscriptionUserInfo]uint64
	nextInfo               uint64
	lastInfoHandles        map[uint64]bool
	catalogFault           error
	pendingInfos           map[uint64]bool
	retireInfos            map[uint64]bool
}

func newServiceAuthority(b serviceStateBackend) *serviceAuthority {
	return &serviceAuthority{backend: b, clocks: map[uint64]time.Time{}}
}
func (s *serviceAuthority) read() (serviceStateSnapshot, error) {
	if s == nil || s.closed {
		return serviceStateSnapshot{}, errServiceStateClosed
	}
	if s.catalogFault != nil {
		return serviceStateSnapshot{}, s.catalogFault
	}
	if s.pending != nil {
		return serviceStateSnapshot{}, errors.New("service command unresolved")
	}
	return s.backend.Meta()
}
func (s *serviceAuthority) mutate(c serviceStateCommand) error {
	v, e := s.read()
	if e != nil {
		return e
	}
	if s.nextCommand == math.MaxUint64 {
		return errors.New("service command exhausted")
	}
	s.nextCommand++
	c.Epoch = v.Epoch
	c.ExpectedRevision = v.Revision
	c.ID = s.nextCommand
	s.pending = &c
	a, e := s.backend.Apply(c)
	if e != nil {
		st, se := s.backend.CommandStatus(c)
		if se != nil || !st.Found || st.Command != c {
			return fmt.Errorf("service command unconfirmed: %v", se)
		}
		a = st.Ack
	}
	if a.Epoch != c.Epoch || a.ID != c.ID || a.Revision != c.ExpectedRevision+1 {
		return errors.New("invalid service acknowledgement")
	}
	if err := s.backend.Acknowledge(a); err != nil {
		st, se := s.backend.CommandStatus(c)
		if se != nil || !st.Found || st.Pending || st.Command != c || st.Ack != a {
			return errors.New("service acknowledgement unconfirmed")
		}
	}
	if c.Kind != 4 && c.Kind != 5 && c.Kind != 6 && c.Kind != 7 {
		if e := s.pruneClocks(); e != nil {
			return e
		}
	}
	s.pending = nil
	return nil
}
func (s *serviceAuthority) baseline(v trafficSample) error {
	if _, e := s.read(); e != nil {
		return e
	}
	c := serviceStateCommand{Kind: 1, HasBaseline: !v.at.IsZero(), Up: v.up, Down: v.down}
	if c.HasBaseline {
		if s.nextToken == math.MaxUint64 {
			return errors.New("service clock exhausted")
		}
		s.nextToken++
		c.TimeToken = s.nextToken
		s.clocks[c.TimeToken] = v.at
		if s.otherClocks == nil {
			s.otherClocks = map[uint64]bool{}
		}
		s.otherClocks[c.TimeToken] = true
	}
	committed := false
	defer func() {
		if !committed && s.pending == nil {
			delete(s.clocks, c.TimeToken)
			delete(s.otherClocks, c.TimeToken)
		}
	}()
	e := s.mutate(c)
	committed = e == nil
	return e
}
func (s *serviceAuthority) samplingError(v string) error {
	return s.mutate(serviceStateCommand{Kind: 2, SamplingError: v})
}
func (s *serviceAuthority) rate(v trafficSample) (int64, int64, error) {
	p, e := s.read()
	if e != nil {
		return 0, 0, e
	}
	seconds := float64(0)
	var previous time.Time
	if p.HasBaseline {
		var ok bool
		previous, ok = s.clocks[p.TimeToken]
		if !ok {
			return 0, 0, errors.New("missing service clock")
		}
		seconds = v.at.Sub(previous).Seconds()
	}
	up, down, ok, e := s.backend.Rate(seconds, v.up, v.down)
	if e != nil {
		return 0, 0, e
	}
	if !ok {
		up, down = rateForGo(trafficSample{at: previous, up: p.Up, down: p.Down}, v)
	}
	return up, down, nil
}

// Caller owns app.mu; no effects may remain in flight during authority transfer.
func (s *serviceAuthority) transferToGo() error {
	if _, e := s.read(); e != nil {
		return e
	}
	cp, e := s.backend.Export()
	if e != nil {
		return e
	}
	live, e := s.backend.Read()
	if e != nil {
		return e
	}
	if validateServiceCheckpoint(cp) != nil || cp.Pending || cp.Snapshot != live {
		return errors.New("service checkpoint mismatch")
	}
	p, err := decodeServicePathState(live.PathPayload)
	if err != nil {
		return err
	}
	if p.Busy || p.LeaseID != 0 {
		return errors.New("path round active")
	}
	if p.AtToken != 0 {
		if _, ok := s.clocks[p.AtToken]; !ok {
			return errors.New("path checkpoint clock missing")
		}
	}
	for _, t := range p.Throttle {
		if _, ok := s.clocks[t.TimeToken]; !ok {
			return errors.New("path throttle clock missing")
		}
	}
	if live.HasBaseline {
		if _, ok := s.clocks[live.TimeToken]; !ok {
			return errors.New("checkpoint clock missing")
		}
	}
	old := s.backend
	h, err := decodeServiceHealthState(cp.Snapshot.HealthPayload)
	if err != nil {
		return err
	}
	if h.Job != nil && h.Job.Running {
		return errors.New("region job active")
	}
	refs, err := uniqueServiceHealthClockRefs(h, live, p)
	if err != nil {
		return err
	}
	for token := range refs {
		if _, ok := s.clocks[token]; !ok {
			return errors.New("health checkpoint clock missing")
		}
	}
	control, e := decodeServiceControlState(cp.Snapshot.ControlPayload)
	if e != nil {
		return e
	}
	catalog, e := decodeServiceCatalogState(cp.Snapshot.CatalogPayload)
	if e != nil {
		return e
	}
	if catalog.HasBackup {
		return errors.New("catalog transaction active")
	}
	if e = s.validateCatalogResources(catalog, cp.LastCommand); e != nil {
		return e
	}
	cp.Snapshot.CatalogPayload = ""
	cp.Snapshot.ControlPayload = ""
	cp.Snapshot.HealthPayload = ""
	s.backend = &goServiceStateBackend{state: cp, health: h, control: control, catalog: catalog}
	s.healthClocks = refs
	return old.Close()
}
func (s *serviceAuthority) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	defer func() {
		s.clocks = nil
		s.healthClocks = nil
		s.otherClocks = nil
		s.pendingHealthClocks = nil
		if s.quotaStore != nil {
			s.quotaStore.release()
			s.quotaStore = nil
		}
		s.infos = nil
		s.infoReverse = nil
		s.lastInfoHandles = nil
		s.pendingInfos = nil
		s.retireInfos = nil
	}()
	return s.backend.Close()
}

// Production newService initializes the selected authority in lifecycle init.
// Literal scratch/fixture apps have no initialized domain and use default Go;
// a closed authority is nonnil and is never replaced here.
func (a *app) serviceStateLocked() *serviceAuthority {
	if a.service == nil {
		a.service = newServiceAuthority(newGoServiceStateBackend())
	}
	return a.service
}

// Revoke the channel before closing the handle. Keep a closed tombstone so no
// later caller can lazily initialize a replacement authority after shutdown.
func (a *app) closeServiceStateLocked() error {
	if a.pathCancel != nil {
		close(a.pathCancel)
		a.pathCancel = nil
	}
	if a.trafficHistoryCancel != nil {
		close(a.trafficHistoryCancel)
		a.trafficHistoryCancel = nil
	}
	if a.service == nil {
		a.service = &serviceAuthority{closed: true}
		return nil
	}
	return a.service.Close()
}

func validateServiceCheckpoint(c serviceStateCheckpoint) error {
	v := c.Snapshot
	bad := func() error { return errors.New("invalid service checkpoint") }
	if cat, e := decodeServiceCatalogState(v.CatalogPayload); e != nil || validateServiceCatalogState(cat) != nil {
		return bad()
	}
	if _, e := decodeServiceChinaState(v.ChinaPayload); e != nil {
		return bad()
	}
	if len(c.LastCommand.ChinaPayload) > 1<<20 {
		return bad()
	}
	if len(c.LastCommand.CatalogPayload) > 16<<20 {
		return bad()
	}
	if len(c.LastCommand.PathPayload) > 8<<20 || len(c.LastCommand.HealthPayload) > 16<<20 || len(c.LastCommand.ControlPayload) > 8<<20 || len(c.LastCommand.SamplingError) > 1<<20 {
		return bad()
	}
	if _, err := decodeServicePathState(v.PathPayload); err != nil {
		return bad()
	}
	if _, err := decodeServiceControlState(v.ControlPayload); err != nil {
		return bad()
	}
	if _, err := decodeServiceHealthState(v.HealthPayload); err != nil {
		return bad()
	}
	if v.Epoch == 0 || v.HasBaseline != (v.TimeToken != 0) || len(v.SamplingError) > 1<<20 {
		return bad()
	}
	if !c.HasLast {
		if c.Pending || v.Revision != 0 || c.LastCommand != (serviceStateCommand{}) || c.LastAck != (serviceStateAck{}) {
			return bad()
		}
		return nil
	}
	q := c.LastCommand
	if q.Epoch != v.Epoch || q.ID == 0 || q.ExpectedRevision == math.MaxUint64 || len(q.SamplingError) > 1<<20 {
		return bad()
	}
	if c.LastAck != (serviceStateAck{q.Epoch, q.ID, q.ExpectedRevision + 1}) || v.Revision != q.ExpectedRevision+1 {
		return bad()
	}
	switch q.Kind {
	case 7:
		st, e := decodeServiceChinaState(v.ChinaPayload)
		if e != nil {
			return bad()
		}
		cmd, e := decodeServiceChinaCommand(q.ChinaPayload)
		if e != nil {
			return bad()
		}
		copy := st
		if e = applyServiceChina(&copy, cmd); e != nil || copy != st {
			return bad()
		}
	case 1:
		if q.HasBaseline != (q.TimeToken != 0) || v.HasBaseline != q.HasBaseline || v.TimeToken != q.TimeToken || v.Up != q.Up || v.Down != q.Down {
			return bad()
		}
	case 6:
		st, e := decodeServiceCatalogState(v.CatalogPayload)
		if e != nil {
			return bad()
		}
		cmd, e := decodeServiceCatalogCommand(q.CatalogPayload)
		if e != nil || validateCatalogLast(st, cmd) != nil {
			return bad()
		}
	case 5:
		v, e := decodeServiceControlState(v.ControlPayload)
		if e != nil {
			return bad()
		}
		c, e := decodeServiceControlCommand(q.ControlPayload)
		if e != nil {
			return bad()
		}
		before := v
		if e = applyServiceControl(&v, c); e != nil || !reflect.DeepEqual(before, v) {
			return bad()
		}
	case 4:
		h, e := decodeServiceHealthState(v.HealthPayload)
		if e != nil {
			return bad()
		}
		c, e := decodeServiceHealthCommand(q.HealthPayload)
		if e != nil {
			return bad()
		}
		if validateServiceHealthLast(h, c) != nil {
			return bad()
		}
	case 3:
		p, e := decodeServicePathState(v.PathPayload)
		if e != nil {
			return bad()
		}
		c, e := decodeServicePathCommand(q.PathPayload)
		if e != nil {
			return bad()
		}
		if validateServicePathLast(p, c) != nil {
			return bad()
		}
	case 2:
		if v.SamplingError != q.SamplingError {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}

func (s *serviceAuthority) pruneClocks() error {
	v, e := s.backend.Meta()
	if e != nil {
		return e
	}
	p, e := decodeServicePathState(v.PathPayload)
	if e != nil {
		return e
	}
	keep := map[uint64]bool{}
	if v.HasBaseline {
		keep[v.TimeToken] = true
	}
	if p.AtToken != 0 {
		keep[p.AtToken] = true
	}
	for _, v := range p.Throttle {
		keep[v.TimeToken] = true
	}
	for token := range s.otherClocks {
		if !keep[token] {
			delete(s.otherClocks, token)
			delete(s.clocks, token)
		}
	}
	return nil
}
