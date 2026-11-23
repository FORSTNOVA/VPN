package main

import (
	"errors"
)

type kernelFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type kernelFlowStart func([]byte) (kernelFlowSession, error)
type kernelFrame struct {
	kind       byte
	a, b       int64
	data, text string
}

func kernelStartInput(q kernelServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"start": 0, "stop": 1}[q.Op]
	if !ok {
		return nil, errors.New("unknown kernel operation")
	}
	return []byte{op}, nil
}
func decodeKernelFrame(b []byte) (kernelFrame, error) {
	r := quotaReader{b: b}
	f := kernelFrame{kind: r.byte(), a: r.integer(), b: r.integer(), data: r.str(), text: r.str()}
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid kernel frame")
	}
	return f, nil
}

type kernelArena struct {
	next                   int64
	bodies                 map[int64][]byte
	errors                 map[int64]error
	options, relay, runner uint64
	generated              string
}

func newKernelArena() *kernelArena {
	return &kernelArena{bodies: map[int64][]byte{}, errors: map[int64]error{}}
}
func (a *kernelArena) replyBytes(f kernelFrame, r kernelReply) ([]byte, error) {
	token := int64(0)
	msg := ""
	if r.Err != nil {
		a.next++
		token = a.next
		a.errors[token] = r.Err
		msg = r.Err.Error()
	}
	if f.kind == 11 {
		a.next++
		token = a.next
		var body []byte
		if r.Body != nil {
			body = append([]byte{}, r.Body...)
		}
		a.bodies[token] = body
	}
	switch f.kind {
	case 12:
		a.options = r.Token
	case 14:
		a.relay = r.Token
	case 16:
		a.runner = r.Token
	case 35:
		a.generated = r.Text
	}
	b := registryBool([]byte{f.kind}, r.Err == nil)
	b = registryBool(b, r.Bool)
	b = registryInt(b, int64(r.Port))
	b = registryInt(b, token)
	b = registryBytes(b, msg)
	b = registryBool(b, r.State != nil)
	if s := r.State; s != nil {
		b = registryBytes(b, s.Mode)
		b = registryBytes(b, s.SubscriptionURL)
		for _, v := range []int{s.ManualCount, s.SocksPort, s.MixedPort, s.ControlPort} {
			b = registryInt(b, int64(v))
		}
		b = registryBool(b, s.KernelPresent)
		b = registryBool(b, s.RelayPresent)
		b = registryBool(b, s.HistoryPresent)
	}
	b = registryBool(b, f.kind == 12 && r.Err == nil)
	if f.kind == 12 && r.Err == nil {
		o := r.Options
		for _, v := range []bool{o.TUN, o.DNSProtection, o.Active, o.ChinaDirect, o.DomesticWeb} {
			b = registryBool(b, v)
		}
	}
	if len(b) > 8<<20 {
		return nil, errors.New("kernel reply limit")
	}
	return b, nil
}
func (a *kernelArena) resultMatches(f kernelFrame, r kernelServiceResult) bool {
	if f.a == 0 {
		return r.Err == nil
	}
	if r.Err == nil {
		return false
	}
	if f.a == 1 {
		e := a.errors[f.b]
		return e != nil && errors.Is(r.Err, e) && r.Err.Error() == f.text
	}
	msg := map[int64]string{2: kernelServiceNoNodes, 3: "could not write the file for the nodes added by hand", 4: "could not allocate a local control port", 5: "could not read the downloaded subscription", 6: "could not write Mihomo configuration", 7: "Mihomo did not become ready; see the local log", 8: "subscription returned no selectable nodes; check the subscription and network"}[f.a]
	return msg != "" && r.Err.Error() == msg
}

func (a *kernelArena) port(f kernelFrame) (kernelEffect, bool) {
	q := kernelEffect{}
	switch f.kind {
	case 1:
		q.Kind = "snapshot"
	case 2:
		q.Kind = "running"
	case 3:
		q.Kind = "tun"
	case 4:
		q.Kind = "profile-matches"
	case 5:
		q.Kind = "fetch"
	case 6:
		q.Kind = "write-manual"
	case 7:
		q.Kind = "mixed-port"
		q.Port = int(f.a)
	case 9:
		q.Kind = "control-port"
	case 11:
		q.Kind = "read-profile"
	case 12:
		q.Kind = "options"
		var ok bool
		q.Body, ok = a.bodies[f.a]
		if !ok {
			return q, false
		}
		q.Bool = f.b != 0
	case 13:
		q.Kind = "china-active"
		q.Bool = f.a != 0
	case 14:
		q.Kind = "relay-new"
		q.Port = int(f.a)
	case 15:
		q.Kind = "write-config"
		q.Text = a.generated
	case 16:
		q.Kind = "runner-new"
	case 17:
		q.Kind = "runner-start"
		q.Token = a.runner
	case 19:
		q.Kind = "ready"
	case 20:
		q.Kind = "owner-check"
		q.Token = a.runner
		q.Port = int(f.a)
	case 21:
		q.Kind = "nodes-ready"
	case 22:
		q.Kind = "privacy"
	case 23:
		q.Kind = "history-start"
	case 24:
		q.Kind = "relay-transfer"
		if f.a != 0 {
			q.Token = a.relay
		}
	case 25:
		q.Kind = "relay-monitor"
		q.Token = a.relay
	case 26:
		q.Kind = "close-local"
		q.Token = a.relay
	case 27:
		q.Kind = "close-app-relay"
	case 29:
		q.Kind = "history-collect"
	case 30:
		q.Kind = "history-stop"
	case 31:
		q.Kind = "runner-stop"
	case 33:
		q.Kind = "remove-config"
	case 34:
		q.Kind = "options-relay"
		q.Token = a.options
		q.Other = a.relay
	case 35:
		q.Kind = "generate"
		q.Token = a.options
	default:
		return q, false
	}
	return q, true
}
