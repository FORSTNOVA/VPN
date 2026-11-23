package main

import (
	"errors"
	"math"
	"sync"
)

// The transport belongs to one authority. No operation chooses a new backend.
type serviceStateTransport interface {
	call(byte, []byte) ([]byte, error)
	close() error
}
type rustServiceStateBackend struct {
	mu        sync.Mutex
	transport serviceStateTransport
	closed    bool
}

func serviceCommandBytes(c serviceStateCommand) []byte {
	b := registryInt(nil, int64(c.Epoch))
	b = registryInt(b, int64(c.ID))
	b = registryInt(b, int64(c.ExpectedRevision))
	b = append(b, c.Kind)
	b = registryBool(b, c.HasBaseline)
	b = registryInt(b, int64(c.TimeToken))
	b = registryInt(b, c.Up)
	b = registryInt(b, c.Down)
	b = registryBytes(b, c.SamplingError)
	b = registryBytes(b, c.PathPayload)
	b = registryBytes(b, c.HealthPayload)
	b = registryBytes(b, c.ControlPayload)
	b = registryBytes(b, c.CatalogPayload)
	return registryBytes(b, c.ChinaPayload)
}
func serviceReadBool(r *quotaReader) bool {
	v := r.byte()
	if v > 1 {
		r.bad = true
	}
	return v == 1
}
func serviceReadSnapshot(r *quotaReader) serviceStateSnapshot {
	return serviceStateSnapshot{Epoch: uint64(r.integer()), Revision: uint64(r.integer()), HasBaseline: serviceReadBool(r), TimeToken: uint64(r.integer()), Up: r.integer(), Down: r.integer(), SamplingError: r.str(), PathPayload: r.str(), HealthPayload: r.str(), ControlPayload: r.str(), CatalogPayload: r.str(), ChinaPayload: r.str()}
}
func serviceReadCommand(r *quotaReader) serviceStateCommand {
	return serviceStateCommand{Epoch: uint64(r.integer()), ID: uint64(r.integer()), ExpectedRevision: uint64(r.integer()), Kind: r.byte(), HasBaseline: serviceReadBool(r), TimeToken: uint64(r.integer()), Up: r.integer(), Down: r.integer(), SamplingError: r.str(), PathPayload: r.str(), HealthPayload: r.str(), ControlPayload: r.str(), CatalogPayload: r.str(), ChinaPayload: r.str()}
}
func serviceReadAck(r *quotaReader) serviceStateAck {
	return serviceStateAck{Epoch: uint64(r.integer()), ID: uint64(r.integer()), Revision: uint64(r.integer())}
}
func serviceWireDone(r *quotaReader) error {
	if r.bad || r.p != len(r.b) {
		return errors.New("invalid service state reply")
	}
	return nil
}
func (b *rustServiceStateBackend) invoke(op byte, p []byte) (*quotaReader, error) {
	if b.closed {
		return nil, errServiceStateClosed
	}
	out, e := b.transport.call(op, p)
	if e != nil {
		return nil, e
	}
	return &quotaReader{b: out}, nil
}
func (b *rustServiceStateBackend) Read() (serviceStateSnapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, e := b.invoke(1, nil)
	if e != nil {
		return serviceStateSnapshot{}, e
	}
	s := serviceReadSnapshot(r)
	return s, serviceWireDone(r)
}
func (b *rustServiceStateBackend) Rate(seconds float64, up, down int64) (int64, int64, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := registryInt(nil, int64(math.Float64bits(seconds)))
	p = registryInt(p, up)
	p = registryInt(p, down)
	r, e := b.invoke(2, p)
	if e != nil {
		return 0, 0, false, e
	}
	u, d := r.integer(), r.integer()
	supported := serviceReadBool(r)
	return u, d, supported, serviceWireDone(r)
}
func (b *rustServiceStateBackend) Apply(c serviceStateCommand) (serviceStateAck, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(c.SamplingError) > 1<<20 || len(c.PathPayload) > 8<<20 || len(c.HealthPayload) > 16<<20 || len(c.ControlPayload) > 8<<20 || len(c.CatalogPayload) > 16<<20 || len(c.ChinaPayload) > 1<<20 {
		return serviceStateAck{}, errors.New("service state payload too large")
	}
	r, e := b.invoke(3, serviceCommandBytes(c))
	if e != nil {
		return serviceStateAck{}, e
	}
	a := serviceReadAck(r)
	if err := serviceWireDone(r); err != nil {
		return a, err
	}
	if a.Epoch != c.Epoch || a.ID != c.ID || c.ExpectedRevision == ^uint64(0) || a.Revision != c.ExpectedRevision+1 {
		return a, errors.New("invalid service state acknowledgement")
	}
	return a, nil
}
func (b *rustServiceStateBackend) CommandStatus(c serviceStateCommand) (serviceStateCommandStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var v serviceStateCommandStatus
	if len(c.SamplingError) > 1<<20 || len(c.PathPayload) > 8<<20 || len(c.HealthPayload) > 16<<20 || len(c.ControlPayload) > 8<<20 || len(c.CatalogPayload) > 16<<20 || len(c.ChinaPayload) > 1<<20 {
		return v, errors.New("service state payload too large")
	}
	r, e := b.invoke(4, serviceCommandBytes(c))
	if e != nil {
		return v, e
	}
	v.Found = serviceReadBool(r)
	if v.Found {
		v.Command = serviceReadCommand(r)
		v.Ack = serviceReadAck(r)
		v.Pending = serviceReadBool(r)
	}
	return v, serviceWireDone(r)
}
func (b *rustServiceStateBackend) Acknowledge(a serviceStateAck) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := registryInt(nil, int64(a.Epoch))
	p = registryInt(p, int64(a.ID))
	p = registryInt(p, int64(a.Revision))
	r, e := b.invoke(6, p)
	if e != nil {
		return e
	}
	return serviceWireDone(r)
}
func (b *rustServiceStateBackend) Export() (serviceStateCheckpoint, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, e := b.invoke(5, nil)
	if e != nil {
		return serviceStateCheckpoint{}, e
	}
	cp := serviceStateCheckpoint{Snapshot: serviceReadSnapshot(r)}
	cp.HasLast = serviceReadBool(r)
	if cp.HasLast {
		cp.LastCommand = serviceReadCommand(r)
		cp.LastAck = serviceReadAck(r)
	}
	cp.Pending = serviceReadBool(r)
	return cp, serviceWireDone(r)
}
func (b *rustServiceStateBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	return b.transport.close()
}

func serviceStateFullCapabilities(version, capabilities uint64) bool {
	return version == 6 && capabilities&63 == 63
}

func (b *rustServiceStateBackend) Meta() (serviceStateSnapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, e := b.invoke(7, nil)
	if e != nil {
		return serviceStateSnapshot{}, e
	}
	v := serviceReadSnapshot(r)
	return v, serviceWireDone(r)
}
func (b *rustServiceStateBackend) HealthQuery(input string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(input) > 16<<20 {
		return "", errors.New("health query too large")
	}
	r, e := b.invoke(8, []byte(input))
	if e != nil {
		return "", e
	}
	return string(r.b), nil
}

func (b *rustServiceStateBackend) ControlQuery() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, e := b.invoke(9, nil)
	if e != nil {
		return "", e
	}
	return string(r.b), nil
}

func (b *rustServiceStateBackend) ControlMeta() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, e := b.invoke(10, nil)
	if e != nil {
		return "", e
	}
	return string(r.b), nil
}

func (b *rustServiceStateBackend) CatalogQuery(input string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(input) > 16<<20 {
		return "", errors.New("catalog query limit")
	}
	r, e := b.invoke(11, []byte(input))
	if e != nil {
		return "", e
	}
	return string(r.b), nil
}

func (b *rustServiceStateBackend) ChinaQuery() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, e := b.invoke(12, nil)
	if e != nil {
		return "", e
	}
	return string(r.b), nil
}
