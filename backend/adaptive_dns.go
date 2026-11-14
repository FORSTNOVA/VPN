package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

var dohIPs = [...]string{"1.1.1.1", "8.8.8.8"}

const slowDNS = 500 * time.Millisecond

type adaptiveDNS struct {
	listener  net.Listener
	server    *dns.Server
	client    *http.Client
	ctx       context.Context
	cancel    context.CancelFunc
	preferred atomic.Int32
}

func newAdaptiveDNS(mixedPort int) (*adaptiveDNS, error) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	proxy := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", mixedPort)}
	ctx, cancel := context.WithCancel(context.Background())
	r := &adaptiveDNS{listener: l, ctx: ctx, cancel: cancel, client: &http.Client{
		Transport:     &http.Transport{Proxy: http.ProxyURL(proxy), ForceAttemptHTTP2: true, IdleConnTimeout: 90 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	ready := make(chan struct{})
	r.server = &dns.Server{Listener: l, Handler: dns.HandlerFunc(r.serve), NotifyStartedFunc: func() { close(ready) }}
	go r.server.ActivateAndServe()
	<-ready
	return r, nil
}

func (r *adaptiveDNS) close() {
	r.cancel()
	_ = r.server.Shutdown()
	r.client.CloseIdleConnections()
}

func (r *adaptiveDNS) exchange(ctx context.Context, index int, q *dns.Msg) (*dns.Msg, error) {
	packet, err := q.Pack()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+dohIPs[index]+"/dns-query", bytes.NewReader(packet))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DoH HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return nil, err
	}
	var ans dns.Msg
	if err = ans.Unpack(body); err != nil {
		return nil, err
	}
	if len(body) > 65535 || !ans.Response || ans.Id != q.Id || len(q.Question) != 1 || len(ans.Question) != 1 || ans.Question[0] != q.Question[0] || ans.Rcode == dns.RcodeServerFailure || ans.Rcode == dns.RcodeRefused {
		return nil, errors.New("invalid or failed DoH response")
	}
	return &ans, nil
}

// Give the preferred resolver a head start. Once the deadline is crossed,
// keep its request alive while trying the other, so a slow backup cannot
// delay an answer that is already on the way.
func (r *adaptiveDNS) resolve(q *dns.Msg) *dns.Msg {
	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
	defer cancel()
	type result struct {
		answer *dns.Msg
		index  int
	}
	done := make(chan result, 2)
	start := func(index int) {
		go func() { answer, _ := r.exchange(ctx, index, q.Copy()); done <- result{answer, index} }()
	}
	first := int(r.preferred.Load())
	start(first)
	timer := time.NewTimer(slowDNS)
	defer timer.Stop()
	pending := 2
	select {
	case res := <-done:
		if res.answer != nil {
			return res.answer
		}
		pending--
	case <-timer.C:
	case <-ctx.Done():
		return nil
	}
	start(1 - first)
	for ; pending > 0; pending-- {
		select {
		case res := <-done:
			if res.answer != nil {
				r.switchTo(first, res.index)
				return res.answer
			}
		case <-ctx.Done():
			return nil
		}
	}
	return nil
}

func (r *adaptiveDNS) switchTo(from, to int) {
	if from != to && r.preferred.CompareAndSwap(int32(from), int32(to)) {
		log.Printf("DNS upstream switched from %s to %s", dohIPs[from], dohIPs[to])
	}
}

func (r *adaptiveDNS) serve(w dns.ResponseWriter, q *dns.Msg) {
	answer := r.resolve(q)
	if answer == nil {
		answer = new(dns.Msg).SetRcode(q, dns.RcodeServerFailure)
	}
	_ = w.WriteMsg(answer)
}

func (r *adaptiveDNS) monitor() {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-tick.C:
			r.checkUpstreams()
		}
	}
}

func (r *adaptiveDNS) checkUpstreams() {
	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
	defer cancel()
	first := int(r.preferred.Load())
	name := dns.Fqdn(probeDomain())
	var elapsed [2]time.Duration
	var healthy [2]bool
	done := make(chan struct{}, 2)
	for i := range dohIPs {
		go func(index int) {
			start := time.Now()
			q := new(dns.Msg).SetQuestion(name, dns.TypeTXT)
			if _, err := r.exchange(ctx, index, q); err == nil {
				healthy[index] = true
				elapsed[index] = time.Since(start)
			}
			done <- struct{}{}
		}(i)
	}
	<-done
	<-done
	active, backup := elapsed[first], elapsed[1-first]
	if healthy[1-first] && (!healthy[first] || (active > slowDNS && backup < active)) {
		r.switchTo(first, 1-first)
	}
}
