package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type dohRoundTrip func(*http.Request) (*http.Response, error)

func (f dohRoundTrip) RoundTrip(q *http.Request) (*http.Response, error) { return f(q) }

func TestAdaptiveDNSFailover(t *testing.T) {
	for _, scenario := range []string{"healthy", "slow", "failed", "both failed"} {
		t.Run(scenario, func(t *testing.T) {
			r := &adaptiveDNS{ctx: context.Background()}
			r.client = &http.Client{Transport: dohRoundTrip(func(req *http.Request) (*http.Response, error) {
				first := req.URL.Host == dohIPs[0]
				if first && scenario == "slow" {
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
				if first && scenario == "failed" || scenario == "both failed" {
					return nil, errors.New("test failure")
				}
				query := new(dns.Msg)
				body, _ := io.ReadAll(req.Body)
				if err := query.Unpack(body); err != nil {
					return nil, err
				}
				reply, err := new(dns.Msg).SetRcode(query, dns.RcodeNameError).Pack()
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(reply))}, err
			})}
			start := time.Now()
			ans := r.resolve(new(dns.Msg).SetQuestion("dns-test.example.", dns.TypeTXT))
			if scenario == "both failed" {
				if ans != nil || r.preferred.Load() != 0 {
					t.Fatal("both upstreams failed")
				}
				return
			}
			if ans == nil || ans.Rcode != dns.RcodeNameError {
				t.Fatal("valid negative answer lost")
			}
			if (r.preferred.Load() == 0) != (scenario == "healthy") {
				t.Fatal("wrong preferred upstream")
			}
			if scenario == "slow" && time.Since(start) < slowDNS {
				t.Fatal("premature failover")
			}
		})
	}
}

func TestAdaptiveDNSRouting(t *testing.T) {
	cfg := generatedConfig(configOptions{TUN: true, trafficPolicy: trafficPolicy{DNSProtection: true}, dnsRelayAddr: "127.0.0.1:43210", Rules: []string{"IP-CIDR,1.1.1.1/32,DIRECT"}})
	for _, fragment := range []string{"tcp://127.0.0.1:43210", "223.5.5.5/dns-query#DIRECT", "8.8.8.8/32,no-resolve)),SmartVPN"} {
		if !strings.Contains(cfg, fragment) {
			t.Fatalf("missing %q", fragment)
		}
	}
	if strings.Index(cfg, "1.1.1.1/32,no-resolve)),SmartVPN") > strings.Index(cfg, "IP-CIDR,1.1.1.1/32,DIRECT") && strings.Contains(cfg, "IP-CIDR,1.1.1.1/32,DIRECT") {
		t.Fatal("subscription rule shadows DoH")
	}
}

func TestDNSMonitorSwitchesSlowUpstream(t *testing.T) {
	r := &adaptiveDNS{ctx: context.Background()}
	r.client = &http.Client{Transport: dohRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == dohIPs[0] {
			time.Sleep(slowDNS + 10*time.Millisecond)
		}
		var query dns.Msg
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if err = query.Unpack(body); err != nil {
			return nil, err
		}
		reply, err := new(dns.Msg).SetReply(&query).Pack()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(reply))}, err
	})}
	r.checkUpstreams()
	if r.preferred.Load() != 1 {
		t.Fatal("background probe did not switch slow resolver")
	}
}
