//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"testing"
	"time"
)

type serviceFaultTransport struct {
	serviceStateTransport
	op                        byte
	before, corrupt, ackWrong bool
	fired                     bool
	counts                    map[byte]int
}

func (t *serviceFaultTransport) call(op byte, b []byte) ([]byte, error) {
	t.counts[op]++
	hit := op == t.op && !t.fired
	if hit {
		t.fired = true
		if t.before {
			return nil, errors.New("before command")
		}
	}
	v, e := t.serviceStateTransport.call(op, b)
	if hit && e == nil {
		if t.ackWrong && len(v) > 0 {
			v[0] ^= 1
			return v, nil
		}
		if t.corrupt {
			return append(v, 255), nil
		}
		return nil, errors.New("after command")
	}
	return v, e
}
func newServiceFault(t *testing.T, op byte, before, corrupt bool) (*serviceAuthority, *serviceFaultTransport, *rustServiceStateBackend) {
	t.Helper()
	base, e := newRustServiceStateBackend()
	if e != nil {
		t.Fatal(e)
	}
	b := base.(*rustServiceStateBackend)
	tr := &serviceFaultTransport{serviceStateTransport: b.transport, op: op, before: before, corrupt: corrupt, counts: map[byte]int{}}
	b.transport = tr
	a := newServiceAuthority(b)
	t.Cleanup(func() { _ = a.Close() })
	return a, tr, b
}
func TestServiceStateActualCommitDeliveryFaults(t *testing.T) {
	for _, op := range []byte{3, 6} {
		for _, corrupt := range []bool{false, true} {
			t.Run(string(rune('0'+op))+map[bool]string{false: "lost", true: "corrupt"}[corrupt], func(t *testing.T) {
				a, tr, b := newServiceFault(t, op, false, corrupt)
				now := time.Now()
				if e := a.baseline(trafficSample{at: now, up: 50, down: 70}); e != nil {
					t.Fatal(e)
				}
				s, e := b.Read()
				if e != nil || s.Revision != 1 || s.TimeToken == 0 || a.pending != nil {
					t.Fatal("not reconciled", s, e)
				}
				if tr.counts[3] != 1 || tr.counts[6] != 1 || tr.counts[4] != 1 {
					t.Fatal("replay or no status", tr.counts)
				}
				if e = a.samplingError("next"); e != nil {
					t.Fatal(e)
				}
				s, _ = b.Read()
				if s.Revision != 2 || s.SamplingError != "next" {
					t.Fatal(s)
				}
			})
		}
	}
}
func TestServiceStateActualSemanticAckCorruption(t *testing.T) {
	a, tr, b := newServiceFault(t, 3, false, false)
	tr.ackWrong = true
	if e := a.samplingError("owned"); e != nil {
		t.Fatal(e)
	}
	s, _ := b.Read()
	if s.Revision != 1 || tr.counts[3] != 1 || tr.counts[4] != 1 {
		t.Fatal("corrupt ack not checked")
	}
}
func TestServiceStateActualUnresolvedFailClosed(t *testing.T) {
	for _, op := range []byte{3, 6} {
		t.Run(string(rune('0'+op)), func(t *testing.T) {
			a, tr, b := newServiceFault(t, op, true, false)
			if e := a.baseline(trafficSample{at: time.Now(), up: 1}); e == nil {
				t.Fatal("unknown accepted")
			}
			before := tr.counts[3]
			for i := 0; i < 1000; i++ {
				if e := a.baseline(trafficSample{at: time.Now(), up: 2}); e == nil {
					t.Fatal("pending accepted")
				}
			}
			if len(a.clocks) > 1 || tr.counts[3] != before {
				t.Fatal("unbounded or replay")
			}
			if e := a.transferToGo(); e == nil {
				t.Fatal("pending transferred")
			}
			if a.backend != b {
				t.Fatal("authority replaced")
			}
		})
	}
}
func TestServiceStateActualCheckpointAndClose(t *testing.T) {
	a, tr, b := newServiceFault(t, 0, false, false)
	now := time.Now()
	if e := a.baseline(trafficSample{at: now, up: 100, down: 200}); e != nil {
		t.Fatal(e)
	}
	if e := a.samplingError("\xff\x00history"); e != nil {
		t.Fatal(e)
	}
	original, _ := b.Read()
	tr.op = 5
	tr.corrupt = true
	tr.fired = false
	if e := a.transferToGo(); e == nil || a.backend != b {
		t.Fatal("bad checkpoint takeover")
	}
	tr.op = 0
	if e := a.transferToGo(); e != nil {
		t.Fatal(e)
	}
	got, e := a.backend.Read()
	if e != nil || got != original {
		t.Fatal("lost state", got, e)
	}
	if _, e = b.Read(); e == nil {
		t.Fatal("old handle live")
	}
	u, d, e := a.rate(trafficSample{at: now.Add(time.Second), up: 110, down: 230})
	if e != nil || u != 10 || d != 30 {
		t.Fatal("monotonic ledger lost", u, d, e)
	}
	if e = a.samplingError("next"); e != nil {
		t.Fatal(e)
	}
	got, _ = a.read()
	if got.Revision != 3 {
		t.Fatal("counter lost", got)
	}
	_ = a.Close()
	n := tr.counts[1]
	if _, e = a.read(); e == nil {
		t.Fatal("closed reopened")
	}
	if tr.counts[1] != n {
		t.Fatal("late call reached native")
	}
}
func TestServiceStateActualLongBoundedLedger(t *testing.T) {
	a, _, b := newServiceFault(t, 0, false, false)
	now := time.Now()
	for i := 0; i < 15000; i++ {
		if e := a.baseline(trafficSample{at: now.Add(time.Duration(i) * time.Millisecond), up: int64(i), down: int64(i * 2)}); e != nil {
			t.Fatal(e)
		}
		if len(a.clocks) != 1 {
			t.Fatal("unbounded clock ledger", len(a.clocks))
		}
	}
	s, e := b.Read()
	if e != nil || s.Revision != 15000 {
		t.Fatal(s, e)
	}
}

func TestServiceStateActualCorruptStatusFailsClosed(t *testing.T) {
	a, tr, b := newServiceFault(t, 4, false, true)
	a.backend = &serviceLoseApply{serviceStateBackend: b}
	if e := a.samplingError("committed-but-unconfirmed"); e == nil {
		t.Fatal("corrupt status accepted")
	}
	if a.pending == nil || tr.counts[3] != 1 || tr.counts[4] != 1 {
		t.Fatal("replayed or cleared pending")
	}
	s, e := b.Read()
	if e != nil || s.Revision != 1 {
		t.Fatal("state mutation lost", s, e)
	}
	if e = a.transferToGo(); e == nil {
		t.Fatal("unknown state transferred")
	}
}

type serviceLoseApply struct{ serviceStateBackend }

func (b *serviceLoseApply) Apply(c serviceStateCommand) (serviceStateAck, error) {
	a, e := b.serviceStateBackend.Apply(c)
	if e != nil {
		return a, e
	}
	return a, errors.New("lost apply reply")
}
