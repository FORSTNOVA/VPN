package main

import (
	"context"
	"github.com/miekg/dns"
	"log"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

var dohIPs = [...]string{"1.1.1.1", "8.8.8.8"}

const slowDNS = 500 * time.Millisecond

type adaptiveDNS struct {
	runtime   *adaptiveRuntime
	listener  net.Listener
	server    *dns.Server
	client    *http.Client
	ctx       context.Context
	cancel    context.CancelFunc
	preferred atomic.Int32
}

func newAdaptiveDNS(mixedPort int) (*adaptiveDNS, error) {
	r := &adaptiveDNS{}
	q := adaptiveServiceRequest{Op: "create", Index: mixedPort}
	e := newAdaptiveEffects(r, nil, q)
	v := runAdaptiveWithEffects(q, e)
	if v.Err != nil {
		return nil, v.Err
	}
	return r, nil
}

func (r *adaptiveDNS) close() {
	q := adaptiveServiceRequest{Op: "close"}
	e := newAdaptiveEffects(r, nil, q)
	if v := runAdaptiveWithEffects(q, e); v.Err != nil {
		panic(v.Err)
	}
}

func (r *adaptiveDNS) exchange(ctx context.Context, index int, msg *dns.Msg) (*dns.Msg, error) {
	q := adaptiveServiceRequest{Op: "exchange", Index: index}
	e := newAdaptiveEffects(r, nil, q)
	e.ctx = ctx
	e.query = msg
	v := runAdaptiveWithEffects(q, e)
	return e.answer, v.Err
}

// Give the preferred resolver a head start. Once the deadline is crossed,
// keep its request alive while trying the other, so a slow backup cannot
// delay an answer that is already on the way.
func (r *adaptiveDNS) resolve(msg *dns.Msg) *dns.Msg {
	q := adaptiveServiceRequest{Op: "resolve"}
	e := newAdaptiveEffects(r, nil, q)
	e.query = msg
	v := runAdaptiveWithEffects(q, e)
	if v.Err != nil {
		log.Print("DNS resolution interrupted")
		return nil
	}
	return e.answer
}

func (r *adaptiveDNS) switchTo(from, to int) {
	q := adaptiveServiceRequest{Op: "switch", From: from, To: to}
	if v := runAdaptiveWithEffects(q, newAdaptiveEffects(r, nil, q)); v.Err != nil {
		log.Print("DNS preference update interrupted")
	}
}

func (r *adaptiveDNS) serve(w dns.ResponseWriter, msg *dns.Msg) {
	q := adaptiveServiceRequest{Op: "serve"}
	e := newAdaptiveEffects(r, nil, q)
	e.query = msg
	e.writer = w
	if v := runAdaptiveWithEffects(q, e); v.Err != nil {
		log.Print("DNS response interrupted")
	}
}

func (r *adaptiveDNS) monitor() {
	q := adaptiveServiceRequest{Op: "monitor"}
	if v := runAdaptiveWithEffects(q, newAdaptiveEffects(r, nil, q)); v.Err != nil {
		log.Print("DNS monitor interrupted")
	}
}

func (r *adaptiveDNS) checkUpstreams() {
	q := adaptiveServiceRequest{Op: "check"}
	if v := runAdaptiveWithEffects(q, newAdaptiveEffects(r, nil, q)); v.Err != nil {
		log.Print("DNS upstream check interrupted")
	}
}
