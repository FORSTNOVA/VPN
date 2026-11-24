package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"strings"
	"sync"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

func (a *app) probeChain(w http.ResponseWriter, r *http.Request) {
	var request struct{ Hops []string }
	if decodeJSON(r.Body, &request) != nil {
		http.Error(w, chainProbeHTTP(0), 400)
		return
	}
	a.runChainProbeService(chainProbeServiceRequest{Op: "probe", Hops: request.Hops, Context: r.Context()}, w)
}

const probeTarget = "https://www.gstatic.com/generate_204"

type probeResult struct {
	EntryAttempt       bool     `json:"entryAttempt,omitempty"`
	DiagnosticsVersion int      `json:"diagnosticsVersion,omitempty"`
	EntryIP            string   `json:"entryIP,omitempty"`
	EntryFamily        string   `json:"entryFamily,omitempty"`
	FailureStage       string   `json:"failureStage,omitempty"`
	Target             string   `json:"target"`
	WarmDelay          int64    `json:"warmDelay,omitempty"`
	WarmError          string   `json:"warmError,omitempty"`
	Hops               []string `json:"hops"`
	Delay              int64    `json:"delay"`
	Error              string   `json:"error,omitempty"`
	Measured           int64    `json:"measured"`
}

func (a *app) currentProxyIndexLocked() (map[string][]map[string]any, error) {
	fx := &appMetadataProjectionEffects{a: a}
	r := runMetadataProjectionWithEffects(metadataProjectionRequest{Op: "current-index"}, fx)
	return fx.index, r.Err
}

func probeFingerprint(proxies []map[string]any) string {
	body, _ := json.Marshal(proxies)
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func (a *app) listProbeResults(w http.ResponseWriter, _ *http.Request) {
	a.runChainProbeService(chainProbeServiceRequest{Op: "list"}, w)
}

func measurePrefix(ctx context.Context, proxies []map[string]any, target string) (int64, error) {
	r := (*app)(nil).runChainProbeService(chainProbeServiceRequest{Op: "measure-prefix", Context: ctx, Proxies: proxies, Target: target, Observation: new(probeObservation)}, nil)
	return r.Cold, r.Err
}
func measureBoth(ctx context.Context, urls []string, proxies []map[string]any, target string, obs *probeObservation) (int64, int64, error) {
	r := (*app)(nil).runChainProbeService(chainProbeServiceRequest{Op: "measure-both", Context: ctx, EntryURLs: urls, Proxies: proxies, Target: target, Observation: obs}, nil)
	return r.Cold, r.Warm, r.Err
}

type probeObservation struct {
	mu                 sync.Mutex
	stage, ip, attempt string
}

func (o *probeObservation) setStage(s string) { o.mu.Lock(); o.stage = s; o.mu.Unlock() }
func (o *probeObservation) address(address string, connected bool) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if connected {
		o.ip = ip.Unmap().String()
	} else {
		o.attempt = ip.Unmap().String()
	}
}

type observedEntry struct {
	C.Dialer
	obs   *probeObservation
	stage string
}

func (d *observedEntry) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.obs.setStage("入口解析或拨号")
	trace := &httptrace.ClientTrace{ConnectStart: func(_, addr string) { d.obs.address(addr, false); d.obs.setStage("入口拨号（尝试地址）") }}
	conn, err := d.Dialer.DialContext(httptrace.WithClientTrace(ctx, trace), network, address)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) || strings.Contains(err.Error(), "dns resolve failed") {
			d.obs.setStage("入口 DNS 解析")
		}
		return nil, err
	}
	d.obs.address(conn.RemoteAddr().String(), true)
	stage := d.stage
	if stage == "" {
		stage = "代理协议握手或链路转发"
	}
	d.obs.setStage(stage)
	return conn, nil
}
func (d *observedEntry) ListenPacket(ctx context.Context, network, address string, remote netip.AddrPort) (net.PacketConn, error) {
	d.obs.address(remote.String(), false)
	d.obs.setStage("入口 UDP 建连（尝试地址）")
	conn, err := d.Dialer.ListenPacket(ctx, network, address, remote)
	if err != nil {
		return nil, err
	}
	d.obs.address(remote.String(), true)
	stage := d.stage
	if stage == "" {
		stage = "代理协议握手或链路转发"
	}
	d.obs.setStage(stage)
	return conn, nil
}

type observedHop struct {
	C.Dialer
	obs   *probeObservation
	stage string
}

func (d *observedHop) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	if d.stage != "" {
		d.obs.setStage(d.stage)
	}
	return conn, nil
}

func timeProbeRequests(ctx context.Context, client *http.Client, target string, obs *probeObservation, budget time.Duration) (int64, int64, error) {
	return timedProbeRequests(ctx, client, target, obs, budget, time.Now())
}
func timedProbeRequests(ctx context.Context, client *http.Client, target string, obs *probeObservation, budget time.Duration, started time.Time) (int64, int64, error) {
	r := (*app)(nil).runChainProbeService(chainProbeServiceRequest{Op: "timed-requests", Context: ctx, Client: client, Target: target, Observation: obs, Budget: budget, Started: started}, nil)
	return r.Cold, r.Warm, r.Err
}
